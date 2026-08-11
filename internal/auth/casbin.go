package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"

	"github.com/ndzuki/release-manager/internal/authorization"
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
m = (r.sub == p.sub || g(r.sub, p.sub, r.dom)) && r.dom == p.dom && keyMatch(r.obj, p.obj) && keyMatch(r.act, p.act)
`

// Enforcer wraps a durable Casbin authorization snapshot.
type Enforcer struct {
	enforcer      *casbin.SyncedEnforcer
	store         store.Store
	metrics       *authorization.Metrics
	logger        *slog.Logger
	refreshMu     sync.Mutex
	mu            sync.RWMutex
	policyHealthy bool
	policyVersion uint64
}

// NewEnforcer creates a Casbin enforcer backed by the durable authorization store.
func NewEnforcer(st store.Store, logger *slog.Logger, metrics ...*authorization.Metrics) (*Enforcer, error) {
	m, err := model.NewModelFromString(casbinModel)
	if err != nil {
		return nil, fmt.Errorf("casbin model: %w", err)
	}

	e, err := casbin.NewSyncedEnforcer(m, &storeAdapter{store: st})
	if err != nil {
		return nil, fmt.Errorf("casbin enforcer: %w", err)
	}
	e.EnableAutoSave(false)
	result := &Enforcer{enforcer: e, store: st, logger: logger}
	if len(metrics) > 0 {
		result.metrics = metrics[0]
	}
	return result, nil
}

// Enforce checks whether sub can perform act on obj within domain.
func (e *Enforcer) Enforce(sub, dom, obj, act string) error {
	if sub == "" || dom == "" || obj == "" || act == "" {
		return newInvalidActorContext(sub, dom, errors.New("authorization fields must not be empty"))
	}
	started := time.Now()
	e.mu.RLock()
	healthy := e.policyHealthy
	allowed, err := e.enforcer.Enforce(sub, dom, obj, act)
	e.mu.RUnlock()
	if e.metrics != nil {
		e.metrics.EnforceDuration.Observe(time.Since(started).Seconds())
	}
	if err != nil {
		return newPolicyUnavailable(sub, dom, obj, act, err)
	}
	if !healthy {
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

// LoadPolicies reloads the durable policy snapshot (AC-027-03 hot reload).
func (e *Enforcer) LoadPolicies(ctx context.Context) error {
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()
	return e.loadPolicies(ctx)
}

func (e *Enforcer) loadPolicies(ctx context.Context) error {
	durable, err := e.store.Authorization().Load(ctx)
	if err != nil {
		e.setPolicyState(false, e.PolicyVersion())
		return fmt.Errorf("load durable authorization snapshot: %w", err)
	}
	rules, compileErr := compileAuthorizationRules(ctx, e.store, durable.Grants, durable.Rules)
	if compileErr != nil {
		e.setPolicyState(false, durable.PolicyVersion)
		return compileErr
	}
	if !reflect.DeepEqual(rules, durable.Rules) {
		mutation := store.AuthorizationPolicyChanged
		if durable.SourceVersion == 0 && len(rules) > 0 {
			mutation = store.AuthorizationMembershipChanged
		}
		durable, err = e.store.Authorization().Apply(ctx, store.AuthorizationApplyCommand{
			ExpectedSourceVersion: durable.SourceVersion,
			ExpectedPolicyVersion: durable.PolicyVersion,
			Mutation:              mutation,
			Rules:                 rules,
		})
		if err != nil {
			e.setPolicyState(false, durable.PolicyVersion)
			return fmt.Errorf("persist authorization rules: %w", err)
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.enforcer.LoadPolicy(); err != nil {
		e.policyHealthy = false
		if e.metrics != nil {
			e.metrics.PolicyHealth.Set(0)
		}
		return fmt.Errorf("load policies: %w", err)
	}
	e.policyHealthy = true
	e.policyVersion = durable.PolicyVersion
	if e.metrics != nil {
		e.metrics.PolicyHealth.Set(1)
		e.metrics.SourceVersion.Set(float64(durable.SourceVersion))
	}
	return nil
}

// RefreshPolicies recompiles current membership and grants, persists them, then reloads Casbin.
func (e *Enforcer) RefreshPolicies(ctx context.Context) (uint64, error) {
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()

	durable, err := e.store.Authorization().Load(ctx)
	if err != nil {
		return e.PolicyVersion(), fmt.Errorf("load authorization state: %w", err)
	}
	rules, err := compileAuthorizationRules(ctx, e.store, durable.Grants, durable.Rules)
	if err != nil {
		return e.PolicyVersion(), err
	}
	if _, err := e.store.Authorization().Apply(ctx, store.AuthorizationApplyCommand{
		ExpectedSourceVersion: durable.SourceVersion,
		ExpectedPolicyVersion: durable.PolicyVersion,
		Mutation:              store.AuthorizationPolicyChanged,
		Rules:                 rules,
	}); err != nil {
		return e.PolicyVersion(), fmt.Errorf("persist refreshed authorization policy: %w", err)
	}
	if err := e.loadPolicies(ctx); err != nil {
		return e.PolicyVersion(), err
	}
	return e.PolicyVersion(), nil
}

// PolicyVersion returns the durable policy snapshot version.
func (e *Enforcer) PolicyVersion() uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.policyVersion
}

// PolicyHealthy reports whether the current durable policy is usable.
func (e *Enforcer) PolicyHealthy() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.policyHealthy
}

func (e *Enforcer) setPolicyState(healthy bool, version uint64) {
	e.mu.Lock()
	e.policyHealthy = healthy
	e.policyVersion = version
	e.mu.Unlock()
	if e.metrics != nil {
		if healthy {
			e.metrics.PolicyHealth.Set(1)
		} else {
			e.metrics.PolicyHealth.Set(0)
		}
	}
}

// AddPolicy persists one direct policy rule and reloads it.
func (e *Enforcer) AddPolicy(sub, dom, obj, act string) error {
	return e.mutateRules(context.Background(), func(rules []store.CasbinRule) []store.CasbinRule {
		return append(rules, store.CasbinRule{PType: "p", V0: sub, V1: dom, V2: obj, V3: act})
	})
}

// AddRoleBinding persists one domain role binding and reloads it.
func (e *Enforcer) AddRoleBinding(user, role, domain string) error {
	return e.mutateRules(context.Background(), func(rules []store.CasbinRule) []store.CasbinRule {
		return append(rules, store.CasbinRule{PType: "g", V0: user, V1: role, V2: domain})
	})
}

// RemoveRoleBinding removes one durable domain role binding and reloads it.
func (e *Enforcer) RemoveRoleBinding(user, role, domain string) error {
	return e.mutateRules(context.Background(), func(rules []store.CasbinRule) []store.CasbinRule {
		filtered := rules[:0]
		for _, rule := range rules {
			if rule.PType == "g" && rule.V0 == user && rule.V1 == role && rule.V2 == domain {
				continue
			}
			filtered = append(filtered, rule)
		}
		return filtered
	})
}

func (e *Enforcer) mutateRules(ctx context.Context, change func([]store.CasbinRule) []store.CasbinRule) error {
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()

	durable, err := e.store.Authorization().Load(ctx)
	if err != nil {
		return err
	}
	rules := append([]store.CasbinRule(nil), durable.Rules...)
	if _, err := e.store.Authorization().Apply(ctx, store.AuthorizationApplyCommand{
		ExpectedSourceVersion: durable.SourceVersion,
		ExpectedPolicyVersion: durable.PolicyVersion,
		Mutation:              store.AuthorizationPolicyChanged,
		Rules:                 change(rules),
	}); err != nil {
		return err
	}
	return e.loadPolicies(ctx)
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

// storeAdapter implements casbin.Adapter over the durable casbin_rule snapshot.
type storeAdapter struct {
	store store.Store
}

func (a *storeAdapter) LoadPolicy(m model.Model) error {
	snapshot, err := a.store.Authorization().Load(context.Background())
	if err != nil {
		return err
	}
	for _, rule := range snapshot.Rules {
		values := []string{rule.V0, rule.V1, rule.V2, rule.V3, rule.V4, rule.V5}
		last := len(values)
		for last > 0 && values[last-1] == "" {
			last--
		}
		if rule.PType == "g" {
			if err := m.AddPolicy("g", rule.PType, values[:last]); err != nil {
				return err
			}
			continue
		}
		if err := m.AddPolicy("p", rule.PType, values[:last]); err != nil {
			return err
		}
	}
	return nil
}

func (a *storeAdapter) SavePolicy(model.Model) error {
	return errors.New("authorization policy writes use AuthorizationStore")
}
func (a *storeAdapter) AddPolicy(_, _ string, _ []string) error {
	return errors.New("authorization policy writes use AuthorizationStore")
}
func (a *storeAdapter) RemovePolicy(_, _ string, _ []string) error {
	return errors.New("authorization policy writes use AuthorizationStore")
}
func (a *storeAdapter) UpdatePolicy(_, _ string, _, _ []string) error {
	return errors.New("authorization policy writes use AuthorizationStore")
}
func (a *storeAdapter) UpdatePolicies(_, _ string, _, _ [][]string) error {
	return errors.New("authorization policy writes use AuthorizationStore")
}
func (a *storeAdapter) RemoveFilteredPolicy(_, _ string, _ int, _ ...string) error {
	return errors.New("authorization policy writes use AuthorizationStore")
}

//nolint:gocyclo // Rule compilation walks membership, grants, and preserved service rules explicitly.
func compileAuthorizationRules(
	ctx context.Context,
	st store.Store,
	grants []store.CapabilityGrant,
	current []store.CasbinRule,
) ([]store.CasbinRule, error) {
	organizations, err := st.Organizations().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	rules := make([]store.CasbinRule, 0)
	for _, organization := range organizations {
		members, err := st.OrgMembers().ListByOrg(ctx, organization.ID)
		if err != nil {
			return nil, fmt.Errorf("list organization members: %w", err)
		}
		for _, member := range members {
			rules = append(rules, store.CasbinRule{PType: "g", V0: member.UserID, V1: string(member.Role), V2: member.OrgID})
			for _, rule := range roleRules(string(member.Role), member.OrgID) {
				rules = append(rules, store.CasbinRule{PType: "p", V0: rule[0], V1: rule[1], V2: rule[2], V3: rule[3]})
			}
			for _, rule := range capabilityRules(member, grants) {
				if rule.V0 == string(member.Role) {
					continue
				}
				rules = append(rules, rule)
			}
		}
	}
	grantRules := make(map[[3]string]struct{}, len(grants))
	for _, grant := range grants {
		grantRules[[3]string{grant.OrganizationID, grant.Subject, string(grant.Action)}] = struct{}{}
	}
	for _, rule := range current {
		if rule.PType == "g" && strings.HasPrefix(rule.V0, "service:") {
			rules = append(rules, rule)
			continue
		}
		if rule.PType != "p" || store.Role(rule.V0).Valid() {
			continue
		}
		if _, managedGrant := grantRules[[3]string{rule.V1, rule.V0, rule.V3}]; managedGrant && rule.V2 == "release" {
			continue
		}
		rules = append(rules, rule)
	}
	return deduplicateRules(rules), nil
}

func deduplicateRules(rules []store.CasbinRule) []store.CasbinRule {
	seen := make(map[store.CasbinRule]struct{}, len(rules))
	result := make([]store.CasbinRule, 0, len(rules))
	for _, rule := range rules {
		if _, ok := seen[rule]; ok {
			continue
		}
		seen[rule] = struct{}{}
		result = append(result, rule)
	}
	return result
}

// roleRules returns the policy rules for a given role within a domain.
func roleRules(role, domain string) [][]string {
	rules := [][]string{}
	switch store.Role(role) {
	case store.RolePlatformAdmin:
		rules = append(rules, []string{role, domain, "*", "*"})
	case store.RoleReleaseAdmin:
		rules = append(rules,
			[]string{role, domain, "organization", "read"},
			[]string{role, domain, "organization", "write"},
			[]string{role, domain, "member", "read"},
			[]string{role, domain, "member", "write"},
			[]string{role, domain, "binding", "read"},
			[]string{role, domain, "binding", "write"},
			[]string{role, domain, "release", "read"},
			[]string{role, domain, "release", "write"},
			[]string{role, domain, "customer", "read"},
			[]string{role, domain, "trust_root", "read"},
		)
	case store.RoleDeployer:
		rules = append(rules,
			[]string{role, domain, "release", "read"},
			[]string{role, domain, "release", "write"},
			[]string{role, domain, "customer", "read"},
			[]string{role, domain, "trust_root", "read"},
		)
	case store.RoleViewer:
		rules = append(rules,
			[]string{role, domain, "organization", "read"},
			[]string{role, domain, "member", "read"},
			[]string{role, domain, "release", "read"},
			[]string{role, domain, "customer", "read"},
			[]string{role, domain, "trust_root", "read"},
		)
	}
	for _, capability := range roleCapabilityRules(store.Role(role), domain) {
		rules = append(rules, []string{capability.V0, capability.V1, capability.V2, capability.V3})
	}
	return rules
}
