package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/ndzuki/release-manager/internal/store"
)

const casbinModel = `
[request_definition]
r = sub, dom, obj, act

[policy_definition]
p = sub, dom, obj, act

[role_definition]
g = _, _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub, r.dom) && r.dom == p.dom && (r.obj == p.obj || p.obj == "*") && (r.act == p.act || p.act == "*")
`

// Enforcer wraps casbin.Enforcer with SQLite-backed policy loading.
type Enforcer struct {
	enforcer      *casbin.SyncedEnforcer
	store         store.Store
	logger        *slog.Logger
	mu            sync.RWMutex
	policyHealthy bool
	policyVersion uint64
}

// NewEnforcer creates a Casbin enforcer backed by the auth store.
func NewEnforcer(st store.Store, logger *slog.Logger) (*Enforcer, error) {
	m, err := model.NewModelFromString(casbinModel)
	if err != nil {
		return nil, fmt.Errorf("casbin model: %w", err)
	}

	e, err := casbin.NewSyncedEnforcer(m, &storeAdapter{store: st})
	if err != nil {
		return nil, fmt.Errorf("casbin enforcer: %w", err)
	}

	return &Enforcer{
		enforcer: e,
		store:    st,
		logger:   logger,
	}, nil
}

// Enforce checks whether sub can perform act on obj within domain.
func (e *Enforcer) Enforce(sub, dom, obj, act string) error {
	if sub == "" || dom == "" || obj == "" || act == "" {
		return newInvalidActorContext(sub, dom, errors.New("authorization fields must not be empty"))
	}
	e.mu.RLock()
	healthy := e.policyHealthy
	allowed, err := e.enforcer.Enforce(sub, dom, obj, act)
	e.mu.RUnlock()
	if err != nil {
		e.logger.Error("casbin enforce error", "error", err)
		return newPolicyUnavailable(sub, dom, obj, act, err)
	}
	if !healthy && act != "read" {
		return newPolicyUnavailable(sub, dom, obj, act, errors.New("policy snapshot is unhealthy"))
	}
	if !allowed {
		return newPermissionDenied(sub, dom, obj, act)
	}
	return nil
}

// CheckBinding verifies that an organization has an active customer binding.
func (e *Enforcer) CheckBinding(ctx context.Context, orgID, customerID string) error {
	if orgID == "" || customerID == "" {
		return newInvalidActorContext("", orgID, errors.New("organization and customer are required"))
	}
	binding, err := e.store.Bindings().GetByOrgAndCustomer(ctx, orgID, customerID)
	if err != nil || binding == nil || binding.Status != store.BindingActive {
		return newDomainBindingMissing(orgID, customerID, err)
	}
	return nil
}

// CheckBindingID verifies that a binding belongs to the organization and is active.
func (e *Enforcer) CheckBindingID(ctx context.Context, orgID, bindingID string) error {
	if orgID == "" || bindingID == "" {
		return newInvalidActorContext("", orgID, errors.New("organization and binding are required"))
	}
	binding, err := e.store.Bindings().Get(ctx, bindingID)
	if err != nil || binding == nil || binding.OrgID != orgID || binding.Status != store.BindingActive {
		return newDomainBindingMissing(orgID, bindingID, err)
	}
	return nil
}

// EnforceServiceActor authorizes a backend service without a JWT.
func (e *Enforcer) EnforceServiceActor(ctx context.Context, service, domain, obj, act, customerID string) error {
	if service == "" || domain == "" {
		return newInvalidActorContext(service, domain, errors.New("service actor and organization are required"))
	}
	if customerID == "" {
		return newInvalidActorContext(service, domain, errors.New("customer is required for service actors"))
	}
	if err := e.CheckBinding(ctx, domain, customerID); err != nil {
		return err
	}
	return e.Enforce("service:"+service, domain, obj, act)
}

// LoadPolicies reloads all policies from the store (AC-027-03 hot reload).
func (e *Enforcer) LoadPolicies(_ context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.enforcer.LoadPolicy(); err != nil {
		e.policyHealthy = false
		return fmt.Errorf("load policies: %w", err)
	}
	e.policyHealthy = true
	e.policyVersion++
	return nil
}

// RefreshPolicies reloads policy immediately and advances the policy version.
func (e *Enforcer) RefreshPolicies(ctx context.Context) (uint64, error) {
	if err := e.LoadPolicies(ctx); err != nil {
		return e.PolicyVersion(), err
	}
	return e.PolicyVersion(), nil
}

// PolicyVersion returns the in-process policy snapshot version.
func (e *Enforcer) PolicyVersion() uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.policyVersion
}

// AddPolicy adds a policy rule for a user/role within an org.
func (e *Enforcer) AddPolicy(sub, dom, obj, act string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err := e.enforcer.AddPolicy(sub, dom, obj, act)
	return err
}

// AddRoleBinding adds a role inheritance: g(user, role, domain).
func (e *Enforcer) AddRoleBinding(user, role, domain string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err := e.enforcer.AddGroupingPolicy(user, role, domain)
	return err
}

// RemoveRoleBinding removes a role binding.
func (e *Enforcer) RemoveRoleBinding(user, role, domain string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err := e.enforcer.RemoveGroupingPolicy(user, role, domain)
	return err
}

// AddServiceActorBinding grants a service actor a role in one organization domain.
func (e *Enforcer) AddServiceActorBinding(service, role, domain string) error {
	if service == "" || domain == "" {
		return newInvalidActorContext(service, domain, errors.New("service actor and organization are required"))
	}
	return e.AddRoleBinding("service:"+service, role, domain)
}

// StartPolicyReloader runs a periodic policy reloader (AC-027-03).
func (e *Enforcer) StartPolicyReloader(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := e.LoadPolicies(ctx); err != nil {
					e.logger.Error("policy reload failed", "error", err)
				}
			}
		}
	}()
}

// storeAdapter implements casbin.Adapter backed by organization membership data.
type storeAdapter struct {
	store store.Store
}

func (a *storeAdapter) LoadPolicy(m model.Model) error {
	ctx := context.Background()
	orgs, err := a.store.Organizations().List(ctx)
	if err != nil {
		return fmt.Errorf("list organizations: %w", err)
	}

	for _, org := range orgs {
		members, err := a.store.OrgMembers().ListByOrg(ctx, org.ID)
		if err != nil {
			return fmt.Errorf("list organization members: %w", err)
		}
		for _, member := range members {
			role := string(member.Role)
			if err := persistRoleBinding(m, "g", member.UserID, role, org.ID); err != nil {
				return err
			}
			for _, rule := range roleRules(role, org.ID) {
				if err := persistPolicy(m, "p", rule); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (a *storeAdapter) SavePolicy(_ model.Model) error                    { return nil }
func (a *storeAdapter) AddPolicy(_, _ string, _ []string) error           { return nil }
func (a *storeAdapter) RemovePolicy(_, _ string, _ []string) error        { return nil }
func (a *storeAdapter) UpdatePolicy(_, _ string, _, _ []string) error     { return nil }
func (a *storeAdapter) UpdatePolicies(_, _ string, _, _ [][]string) error { return nil }
func (a *storeAdapter) RemoveFilteredPolicy(_, _ string, _ int, _ ...string) error {
	return nil
}

func persistRoleBinding(m model.Model, ptype, v0, v1, v2 string) error {
	return m.AddPolicy("g", ptype, []string{v0, v1, v2})
}

func persistPolicy(m model.Model, ptype string, rule []string) error {
	return m.AddPolicy("p", ptype, rule)
}

// roleRules returns the policy rules for a given role within a domain.
func roleRules(role, domain string) [][]string {
	switch store.Role(role) {
	case store.RolePlatformAdmin:
		return [][]string{{role, domain, "*", "*"}}
	case store.RoleReleaseAdmin:
		return [][]string{
			{role, domain, "organization", "read"},
			{role, domain, "organization", "write"},
			{role, domain, "member", "read"},
			{role, domain, "member", "write"},
			{role, domain, "binding", "read"},
			{role, domain, "binding", "write"},
			{role, domain, "release", "read"},
			{role, domain, "release", "write"},
			{role, domain, "operator", "read"},
			{role, domain, "operator", "enroll"},
			{role, domain, "operator", "revoke"},
			{role, domain, "customer", "read"},
		}
	case store.RoleDeployer:
		return [][]string{
			{role, domain, "release", "read"},
			{role, domain, "release", "write"},
			{role, domain, "operator", "read"},
			{role, domain, "customer", "read"},
		}
	case store.RoleViewer:
		return [][]string{
			{role, domain, "organization", "read"},
			{role, domain, "member", "read"},
			{role, domain, "release", "read"},
			{role, domain, "operator", "read"},
			{role, domain, "customer", "read"},
		}
	}
	return nil
}
