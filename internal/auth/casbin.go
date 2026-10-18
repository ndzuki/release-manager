package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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

// ErrEnforcerClosed reports that the enforcer was closed, so it no longer
// loads or refreshes policy state from its store.
var ErrEnforcerClosed = errors.New("authorization enforcer is closed")

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
	// Lifecycle of the background policy reloader. closeOnce/closed keep Close
	// idempotent and fail-closed; reloadCancel and reloadDone let Close stop the
	// goroutine and wait for an in-flight reload before the store goes away.
	lifecycleMu  sync.Mutex
	closeOnce    sync.Once
	closeDone    chan struct{}
	reloadCancel context.CancelFunc
	reloadDone   chan struct{}
	closed       atomic.Bool
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
	result := &Enforcer{enforcer: e, store: st, logger: logger, closeDone: make(chan struct{})}
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
// After Close it fails closed without touching the store.
func (e *Enforcer) LoadPolicies(ctx context.Context) error {
	if e.closed.Load() {
		return ErrEnforcerClosed
	}
	if err := e.lockRefresh(); err != nil {
		return err
	}
	defer e.refreshMu.Unlock()
	return e.loadPolicies(ctx)
}

// lockRefresh serialises policy refreshes and refuses new ones once Close has
// run. Close takes the same lock as a barrier, so after it returns no caller
// that passed this check can still be reading the store from a refresh.
func (e *Enforcer) lockRefresh() error {
	e.refreshMu.Lock()
	if e.closed.Load() {
		e.refreshMu.Unlock()
		return ErrEnforcerClosed
	}
	return nil
}

func (e *Enforcer) loadPolicies(ctx context.Context) error {
	durable, err := e.store.Authorization().Load(ctx)
	if err != nil {
		e.setPolicyUnavailable(e.PolicyVersion())
		return fmt.Errorf("load durable authorization snapshot: %w", err)
	}
	if durable == nil {
		e.setPolicyUnavailable(e.PolicyVersion())
		return errors.New("load durable authorization snapshot: store returned no snapshot")
	}
	rules, compileErr := compileAuthorizationRules(ctx, e.logger, e.store, durable.Grants, durable.Rules)
	if compileErr != nil {
		e.setPolicyUnavailable(durable.PolicyVersion)
		return compileErr
	}
	if !reflect.DeepEqual(rules, durable.Rules) {
		mutation := store.AuthorizationPolicyChanged
		if durable.SourceVersion == 0 && len(rules) > 0 {
			mutation = store.AuthorizationMembershipChanged
		}
		// Keep the previous snapshot until Apply succeeds: Apply returns a nil
		// snapshot on every error path (both SQLite and PostgreSQL), so
		// assigning its result to durable would turn the error branch into a
		// nil dereference (CI panic at TASK-365: casbin.go:161 while a reloader
		// tick raced the store teardown).
		updated, applyErr := e.store.Authorization().Apply(ctx, store.AuthorizationApplyCommand{
			ExpectedSourceVersion: durable.SourceVersion,
			ExpectedPolicyVersion: durable.PolicyVersion,
			Mutation:              mutation,
			Rules:                 rules,
		})
		if applyErr != nil {
			e.setPolicyUnavailable(durable.PolicyVersion)
			return fmt.Errorf("persist authorization rules: %w", applyErr)
		}
		if updated == nil {
			e.setPolicyUnavailable(durable.PolicyVersion)
			return errors.New("persist authorization rules: store returned no snapshot")
		}
		durable = updated
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
	if e.closed.Load() {
		return e.PolicyVersion(), ErrEnforcerClosed
	}
	if err := e.lockRefresh(); err != nil {
		return e.PolicyVersion(), err
	}
	defer e.refreshMu.Unlock()

	durable, err := e.store.Authorization().Load(ctx)
	if err != nil {
		return e.PolicyVersion(), fmt.Errorf("load authorization state: %w", err)
	}
	rules, err := compileAuthorizationRules(ctx, e.logger, e.store, durable.Grants, durable.Rules)
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

// setPolicyUnavailable records that the policy snapshot is unusable while
// keeping the reported version at the last durable value.
func (e *Enforcer) setPolicyUnavailable(version uint64) {
	e.mu.Lock()
	e.policyHealthy = false
	e.policyVersion = version
	e.mu.Unlock()
	if e.metrics != nil {
		e.metrics.PolicyHealth.Set(0)
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
	if e.closed.Load() {
		return ErrEnforcerClosed
	}
	if err := e.lockRefresh(); err != nil {
		return err
	}
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
//
// The goroutine stops when ctx is cancelled or when Close is called. Close
// waits for an in-flight reload, so callers must Close the enforcer before
// closing the underlying store. Calling it more than once, or after Close, is
// a no-op.
func (e *Enforcer) StartPolicyReloader(ctx context.Context, interval time.Duration) {
	e.lifecycleMu.Lock()
	if e.closed.Load() || e.reloadCancel != nil {
		e.lifecycleMu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	e.reloadCancel = cancel
	e.reloadDone = done
	e.lifecycleMu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if err := e.LoadPolicies(runCtx); err != nil && runCtx.Err() == nil {
					e.logger.Error("policy reload failed", "error", err)
				}
			}
		}
	}()
}

// Close stops the policy reloader and waits for an in-flight reload to finish.
// It must be called before the underlying store is closed; afterwards policy
// loads fail closed with ErrEnforcerClosed instead of touching the store.
func (e *Enforcer) Close() {
	e.closeOnce.Do(func() {
		e.closed.Store(true)
		e.lifecycleMu.Lock()
		cancel, done := e.reloadCancel, e.reloadDone
		e.lifecycleMu.Unlock()
		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}
		// Barrier: a caller that passed the closed check before this Close may
		// still hold refreshMu. Wait it out, so Close returning really means
		// the store has no policy reader left. closeDone is published under the
		// same lock, so a waiter cannot observe a half-closed lifecycle.
		e.refreshMu.Lock()
		close(e.closeDone)
		e.refreshMu.Unlock()
	})
	<-e.closeDone
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

// UpdatePolicy and UpdatePolicies are deliberately NOT implemented: casbin only reaches them
// through persist.UpdatableAdapter, which this adapter does not satisfy, and this repo builds a
// SyncedEnforcer rather than a DistributedEnforcer, so nothing selects them.
//
// Two traps live here. First, casbin asserts that interface WITHOUT the comma-ok form
// (enforcer_distributed.go), so an adapter that still does not satisfy it makes that path panic
// rather than no-op -- implement persist.UpdatableAdapter in full if a distributed enforcer is
// ever adopted. Second, NOTHING here is checked at compile time: NewSyncedEnforcer takes
// ...interface{} and type-asserts at run time, so deleting one of persist.Adapter's five methods
// (LoadPolicy, SavePolicy, AddPolicy, RemovePolicy, RemoveFilteredPolicy) still builds and only
// fails when the enforcer is constructed. Keep the adapter's method set aligned with that
// interface by inspection, not by hoping the build catches it.
func (a *storeAdapter) RemoveFilteredPolicy(_, _ string, _ int, _ ...string) error {
	return errors.New("authorization policy writes use AuthorizationStore")
}

//nolint:gocyclo // Rule compilation walks membership, grants, and preserved service rules explicitly.
func compileAuthorizationRules(
	ctx context.Context,
	logger *slog.Logger,
	st store.Store,
	grants []store.CapabilityGrant,
	current []store.CasbinRule,
) ([]store.CasbinRule, error) {
	if logger == nil {
		logger = slog.Default()
	}
	organizations, err := st.Organizations().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	if len(organizations) == 0 {
		// The projection is derived from THIS store's organizations and
		// org_members rows (release-auth owns them). A service that opens its own
		// empty database therefore compiles an empty policy and denies every
		// Casbin procedure, including the bootstrap reads — which is invisible
		// except as a wall of permission_denied. TASK-104: say it out loud.
		logger.Warn("authorization projection is empty: no organizations are visible in this service's database",
			"contract", "release-auth and release-orchestrator must share one authority database",
			"docs", "docs/dev-environment.md")
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
			// Bundles are the artifact half of a release, and mapServiceToObject
			// gives BundleService its own object, so release_admin needs an
			// explicit rule to read them. Without it every BundleService read
			// was admin-only, and the REQ-066 E2E runner — seeded as
			// release_admin — could not validate the bundle artifact it exists
			// to check (real smoke 2026-09-11: GetBundle/ListBundles answered
			// permission_denied while every sibling read succeeded). Read only:
			// bundle ingress stays admin-only.
			[]string{role, domain, "bundle", "read"},
			[]string{role, domain, "operator", "read"},
			[]string{role, domain, "operator", "enroll"},
			[]string{role, domain, "operator", "revoke"},
			[]string{role, domain, "customer", "read"},
			[]string{role, domain, "trust_root", "read"},
			// Audit trail (REQ-029): every role may read its own organization's
			// trail, while emitting events and registering exports stay with the
			// administrator roles. The audit object exists so the release-api
			// audit surface can ask release-auth for a role-aware decision
			// (ADR-021) instead of trusting the JWT role claim.
			[]string{role, domain, "audit", "read"},
			[]string{role, domain, "audit", "write"},
		)
	case store.RoleDeployer:
		rules = append(rules,
			[]string{role, domain, "release", "read"},
			[]string{role, domain, "release", "write"},
			[]string{role, domain, "operator", "read"},
			[]string{role, domain, "customer", "read"},
			[]string{role, domain, "trust_root", "read"},
			[]string{role, domain, "audit", "read"},
		)
	case store.RoleViewer:
		rules = append(rules,
			[]string{role, domain, "organization", "read"},
			[]string{role, domain, "member", "read"},
			[]string{role, domain, "release", "read"},
			[]string{role, domain, "operator", "read"},
			[]string{role, domain, "customer", "read"},
			[]string{role, domain, "trust_root", "read"},
			[]string{role, domain, "audit", "read"},
		)
	}
	for _, capability := range roleCapabilityRules(store.Role(role), domain) {
		rules = append(rules, []string{capability.V0, capability.V1, capability.V2, capability.V3})
	}
	return rules
}
