package authorization

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/store"
)

// StoreAuthorizer is an in-process adapter for tests and single-process development.
// Production Orchestrator uses Module and the Auth Connect producer.
type StoreAuthorizer struct {
	store store.Store
}

// NewStoreAuthorizer creates an authorizer over one authoritative Store.
func NewStoreAuthorizer(st store.Store) *StoreAuthorizer { return &StoreAuthorizer{store: st} }

//nolint:gocyclo // Scope, binding, org/customer, membership, grant, and role gates are explicit.
func (a *StoreAuthorizer) AuthorizeWrite(ctx context.Context, actor authctx.Actor, customerID string, action store.AuthorizationAction) error {
	if a.store == nil || customerID == "" || !action.Valid() || actor.OrganizationID == "" {
		return invalidActorError("actor organization, customer, and action are required")
	}
	if actor.Service != "" && actor.OrganizationID == "" {
		return invalidActorError("service actor organization is required")
	}
	binding, err := a.store.Bindings().GetByOrgAndCustomer(ctx, actor.OrganizationID, customerID)
	if err != nil || binding.Status != store.BindingActive {
		return permissionDeniedError(0, 0, 0)
	}
	organization, err := a.store.Organizations().Get(ctx, actor.OrganizationID)
	if err != nil || organization.Status != store.OrgActive {
		return permissionDeniedError(0, 0, 0)
	}
	customer, err := a.store.Customers().Get(ctx, customerID)
	if err != nil || customer.Status != store.CustomerActive {
		return permissionDeniedError(0, 0, 0)
	}
	subject := actor.UserID
	role := store.Role("")
	if actor.Service != "" {
		subject = "service:" + actor.Service
	} else {
		member, memberErr := a.store.OrgMembers().Get(ctx, actor.OrganizationID, actor.UserID)
		if memberErr != nil {
			return permissionDeniedError(0, 0, 0)
		}
		role = member.Role
	}
	snapshot, err := a.store.Authorization().Load(ctx)
	if err != nil {
		return staleError(0, 0, 0)
	}
	for _, grant := range snapshot.Grants {
		if grant.OrganizationID == actor.OrganizationID && grant.Subject == subject && grant.Action == action && !grant.Revoked {
			recordFence(ctx, snapshot.SourceVersion)
			return nil
		}
	}
	if roleAllows(role, action) {
		recordFence(ctx, snapshot.SourceVersion)
		return nil
	}
	return permissionDeniedError(snapshot.SourceVersion, snapshot.SourceVersion, snapshot.PolicyVersion)
}

func (a *StoreAuthorizer) Snapshot(ctx context.Context, organizationID, customerID string) (*Snapshot, error) {
	checkpoint, err := a.store.Authorization().GetCheckpoint(ctx, organizationID, customerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("authorization snapshot unavailable"))
	}
	return &Snapshot{
		OrganizationID: organizationID, CustomerID: customerID, SourceVersion: checkpoint.SourceVersion,
		PolicyVersion: checkpoint.PolicyVersion, Checkpoint: checkpoint.SourceVersion, Fresh: checkpoint.Fresh,
	}, nil
}

func roleAllows(role store.Role, action store.AuthorizationAction) bool {
	switch role {
	case store.RolePlatformAdmin:
		return true
	case store.RoleReleaseAdmin:
		return action == store.AuthorizationExecuteEmergency || action == store.AuthorizationCreateValues || action == store.AuthorizationApproveValues
	case store.RoleDeployer:
		return action == store.AuthorizationCreateValues
	default:
		return false
	}
}

var _ Authorizer = (*StoreAuthorizer)(nil)
