package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type approvalFixture struct {
	svc        *Service
	st         *sqlitestore.Store
	ctx        context.Context
	orgID      string
	customerID string
	defID      string
	creatorID  string
	adminID    string
	revisionID string
}

type advancingAuthorizer struct {
	delegate authorization.Authorizer
	store    store.Store
}

func (a *advancingAuthorizer) AuthorizeWrite(ctx context.Context, actor authctx.Actor, customerID string, action store.AuthorizationAction) error {
	if err := a.delegate.AuthorizeWrite(ctx, actor, customerID, action); err != nil {
		return err
	}
	snapshot, err := a.store.Authorization().Load(ctx)
	if err != nil {
		return err
	}
	_, err = a.store.Authorization().Apply(ctx, store.AuthorizationApplyCommand{
		ExpectedSourceVersion: snapshot.SourceVersion,
		ExpectedPolicyVersion: snapshot.PolicyVersion,
		Mutation:              store.AuthorizationMembershipChanged,
	})
	return err
}

func (a *advancingAuthorizer) Snapshot(ctx context.Context, organizationID, customerID string) (*authorization.Snapshot, error) {
	return a.delegate.Snapshot(ctx, organizationID, customerID)
}
func newApprovalFixture(t *testing.T) approvalFixture {
	t.Helper()
	st := sqlitestore.OpenTest(t)
	ctx := context.Background()
	orgID := "org-068"
	customerID := "customer-068"
	defID := "definition-068"
	creatorID := "creator-068"
	adminID := "admin-068"
	owner := orgID

	require.NoError(t, st.Customers().Create(ctx, &store.Customer{
		ID: customerID, Name: "Customer 068", Slug: "customer-068",
	}))
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "Organization 068"}))
	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: "binding-068", OrgID: orgID, CustomerID: customerID,
	}))
	for userID, role := range map[string]store.Role{
		creatorID: store.RoleDeployer,
		adminID:   store.RoleReleaseAdmin,
	} {
		require.NoError(t, st.Users().Create(ctx, &store.User{ID: userID, Username: userID, PasswordHash: "unused"}))
		require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: orgID, UserID: userID, Role: role}))
	}
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: defID, Name: "definition-068", CustomerID: customerID, ClusterID: "cluster-068",
		ReleaseName: "release-068", Status: store.DefStatusActive, OwnerOrganizationID: &owner,
	}, nil))

	revisionID := "revision-068"
	require.NoError(t, st.Values().Create(ctx, &store.ValuesRevision{
		ID: revisionID, ReleaseDefinitionID: defID, Version: 1, StateVersion: 1,
		Status: store.ValuesStatusDraft, CanonicalDocument: []byte(`{"replicas":1}`), Digest: "sha256:068",
		CreatedByUserID: creatorID,
	}))
	return approvalFixture{
		svc: NewService(st, nil, "staging", nil, authorization.NewStoreAuthorizer(st), slog.New(slog.DiscardHandler)), st: st, ctx: ctx,
		orgID: orgID, customerID: customerID, defID: defID, creatorID: creatorID, adminID: adminID,
		revisionID: revisionID,
	}
}

func TestValuesApprovalAuthorizationFenceRejectsVersionAdvance(t *testing.T) {
	f := newApprovalFixture(t)
	f.svc.authorizer = &advancingAuthorizer{delegate: authorization.NewStoreAuthorizer(f.st), store: f.st}
	_, err := f.svc.SubmitValuesRevision(f.actorContext(f.creatorID, store.RoleDeployer), f.submitRequest("fence-stale"))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.Equal(t, "authorization_snapshot_stale", approvalReasonCode(t, err))
	revision, getErr := f.st.Values().Get(f.ctx, f.revisionID)
	require.NoError(t, getErr)
	assert.Equal(t, store.ValuesStatusDraft, revision.Status)
	decisions, listErr := f.st.ValuesApprovalEvidence().ListDecisions(f.ctx, f.revisionID)
	require.NoError(t, listErr)
	assert.Empty(t, decisions)
}

func (f approvalFixture) actorContext(userID string, roles ...store.Role) context.Context {
	roleNames := make([]string, 0, len(roles))
	for _, role := range roles {
		roleNames = append(roleNames, string(role))
	}
	return authctx.WithActor(f.ctx, authctx.Actor{
		UserID: userID, OrganizationID: f.orgID, Roles: roleNames,
	})
}

func (f approvalFixture) submitRequest(key string) *connect.Request[orchestratorv1.SubmitValuesRevisionRequest] {
	req := connect.NewRequest(&orchestratorv1.SubmitValuesRevisionRequest{
		RevisionId: f.revisionID, ExpectedStateVersion: 1, Comment: "ready",
	})
	req.Header().Set("Idempotency-Key", key)
	return req
}

func (f approvalFixture) approveRequest(key string, version int64) *connect.Request[orchestratorv1.ApproveValuesRevisionRequest] {
	req := connect.NewRequest(&orchestratorv1.ApproveValuesRevisionRequest{
		RevisionId: f.revisionID, ExpectedStateVersion: version, Comment: "approved",
	})
	req.Header().Set("Idempotency-Key", key)
	return req
}

func (f approvalFixture) rejectRequest(key string, version int64, reason string) *connect.Request[orchestratorv1.RejectValuesRevisionRequest] {
	req := connect.NewRequest(&orchestratorv1.RejectValuesRevisionRequest{
		RevisionId: f.revisionID, ExpectedStateVersion: version, Reason: reason,
	})
	req.Header().Set("Idempotency-Key", key)
	return req
}

func (f approvalFixture) submit(t *testing.T) *connect.Response[orchestratorv1.ValuesRevisionDecisionResponse] {
	t.Helper()
	resp, err := f.svc.SubmitValuesRevision(f.actorContext(f.creatorID, store.RoleDeployer), f.submitRequest("submit-068"))
	require.NoError(t, err)
	return resp
}

func TestValuesApproval_SuccessPathsAndEvidence(t *testing.T) {
	t.Run("AC-068-01 submit writes decision and both outboxes", func(t *testing.T) {
		f := newApprovalFixture(t)
		resp := f.submit(t)
		assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_PENDING_APPROVAL, resp.Msg.GetNewState())
		assert.EqualValues(t, 2, resp.Msg.GetRevision().GetStateVersion())
		assert.NotNil(t, resp.Msg.GetRevision().GetSubmittedAt())

		decisions, err := f.st.ValuesApprovalEvidence().ListDecisions(f.ctx, f.revisionID)
		require.NoError(t, err)
		require.Len(t, decisions, 1)
		assert.Equal(t, store.ValuesDecisionSubmitted, decisions[0].Action)

		auditEntries := approvalAuditEntries(t, f)
		require.Len(t, auditEntries, 1)
		var auditPayload map[string]any
		require.NoError(t, json.Unmarshal(auditEntries[0].PayloadJSON, &auditPayload))
		assert.NotContains(t, auditPayload, "comment")
		assert.NotEmpty(t, auditPayload["comment_hash"])
		assert.EqualValues(t, len([]byte("ready")), auditPayload["comment_length"])

		notificationEntries := approvalNotificationEntries(t, f)
		require.Len(t, notificationEntries, 1)
		var notificationPayload map[string]any
		require.NoError(t, json.Unmarshal(notificationEntries[0].PayloadJSON, &notificationPayload))
		assert.Equal(t, f.creatorID, notificationPayload["created_by_user_id"])
		assert.NotEmpty(t, notificationPayload["submitted_at"])
		assert.NotEmpty(t, notificationPayload["request_id"])
		assert.NotContains(t, notificationPayload, "comment")
		assert.NotContains(t, notificationPayload, "actor_user_id")
	})

	t.Run("AC-068-02 approve supersedes old and updates pointer", func(t *testing.T) {
		f := newApprovalFixture(t)
		oldID := "old-approved-068"
		_, err := f.st.DB().ExecContext(f.ctx, `DELETE FROM values_revisions WHERE id = ?`, f.revisionID)
		require.NoError(t, err)
		require.NoError(t, f.st.Values().Create(f.ctx, &store.ValuesRevision{
			ID: oldID, ReleaseDefinitionID: f.defID, Version: 1, StateVersion: 1,
			Status: store.ValuesStatusApproved, CanonicalDocument: []byte(`{"replicas":0}`), Digest: "sha256:old",
			CreatedByUserID: "old-creator",
		}))
		require.NoError(t, f.st.Values().Create(f.ctx, &store.ValuesRevision{
			ID: f.revisionID, ReleaseDefinitionID: f.defID, Version: 2, StateVersion: 1,
			Status: store.ValuesStatusDraft, CanonicalDocument: []byte(`{"replicas":1}`), Digest: "sha256:068",
			ParentRevisionID: oldID, CreatedByUserID: f.creatorID,
		}))
		f.submit(t)
		resp, err := f.svc.ApproveValuesRevision(
			f.actorContext(f.adminID, store.RoleReleaseAdmin), f.approveRequest("approve-068", 2),
		)
		require.NoError(t, err)
		assert.Equal(t, []string{oldID}, resp.Msg.GetSupersededRevisionIds())
		old, err := f.st.Values().Get(f.ctx, oldID)
		require.NoError(t, err)
		assert.Equal(t, store.ValuesStatusSuperseded, old.Status)
		definition, err := f.st.Definitions().Get(f.ctx, f.defID)
		require.NoError(t, err)
		require.NotNil(t, definition.ApprovedRevisionID)
		assert.Equal(t, f.revisionID, *definition.ApprovedRevisionID)
		decisions, err := f.st.ValuesApprovalEvidence().ListDecisions(f.ctx, f.revisionID)
		require.NoError(t, err)
		require.Len(t, decisions, 2)
		assert.Equal(t, store.ValuesDecisionApproved, decisions[1].Action)
		auditEntries := approvalAuditEntries(t, f)
		require.Len(t, auditEntries, 2)
		assert.Equal(t, "ValuesRevisionApproved", auditEntries[1].EventType)
		notificationEntries := approvalNotificationEntries(t, f)
		require.Len(t, notificationEntries, 2)
		assert.Equal(t, "ValuesRevisionApproved", notificationEntries[1].EventType)
	})

	t.Run("AC-068-03 reject persists reason in notification event", func(t *testing.T) {
		f := newApprovalFixture(t)
		f.submit(t)
		resp, err := f.svc.RejectValuesRevision(
			f.actorContext(f.adminID, store.RoleReleaseAdmin), f.rejectRequest("reject-068", 2, "needs changes"),
		)
		require.NoError(t, err)
		assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_REJECTED, resp.Msg.GetNewState())
		notificationEntries := approvalNotificationEntries(t, f)
		require.Len(t, notificationEntries, 2)
		var notificationPayload map[string]any
		require.NoError(t, json.Unmarshal(notificationEntries[1].PayloadJSON, &notificationPayload))
		assert.Equal(t, "needs changes", notificationPayload["reason"])
		auditEntries := approvalAuditEntries(t, f)
		require.Len(t, auditEntries, 2)
		var auditPayload map[string]any
		require.NoError(t, json.Unmarshal(auditEntries[1].PayloadJSON, &auditPayload))
		assert.NotContains(t, auditPayload, "reason")
		assert.NotEmpty(t, auditPayload["reason_hash"])
		assert.EqualValues(t, len([]byte("needs changes")), auditPayload["reason_length"])
		decisions, err := f.st.ValuesApprovalEvidence().ListDecisions(f.ctx, f.revisionID)
		require.NoError(t, err)
		require.Len(t, decisions, 2)
		assert.Equal(t, store.ValuesDecisionRejected, decisions[1].Action)
		assert.Equal(t, "ValuesRevisionRejected", auditEntries[1].EventType)
		assert.Equal(t, "ValuesRevisionRejected", notificationEntries[1].EventType)
	})

	t.Run("AC-068-04 rejected parent remains unchanged", func(t *testing.T) {
		f := newApprovalFixture(t)
		f.submit(t)
		_, err := f.svc.RejectValuesRevision(f.actorContext(f.adminID, store.RoleReleaseAdmin), f.rejectRequest("reject-parent", 2, "redo"))
		require.NoError(t, err)
		childID := "child-068"
		require.NoError(t, f.st.Values().Create(f.ctx, &store.ValuesRevision{
			ID: childID, ReleaseDefinitionID: f.defID, Version: 2, StateVersion: 1,
			Status: store.ValuesStatusDraft, CanonicalDocument: []byte(`{"replicas":2}`), Digest: "sha256:child",
			ParentRevisionID: f.revisionID, CreatedByUserID: f.creatorID,
		}))
		submit := connect.NewRequest(&orchestratorv1.SubmitValuesRevisionRequest{
			RevisionId: childID, ExpectedStateVersion: 1, Comment: "corrected",
		})
		submit.Header().Set("Idempotency-Key", "submit-rejected-child")
		_, err = f.svc.SubmitValuesRevision(f.actorContext(f.creatorID, store.RoleDeployer), submit)
		require.NoError(t, err)
		approve := connect.NewRequest(&orchestratorv1.ApproveValuesRevisionRequest{
			RevisionId: childID, ExpectedStateVersion: 2, Comment: "approved corrected revision",
		})
		approve.Header().Set("Idempotency-Key", "approve-rejected-child")
		_, err = f.svc.ApproveValuesRevision(f.actorContext(f.adminID, store.RoleReleaseAdmin), approve)
		require.NoError(t, err)

		child, err := f.st.Values().Get(f.ctx, childID)
		require.NoError(t, err)
		assert.Equal(t, f.revisionID, child.ParentRevisionID)
		assert.Equal(t, store.ValuesStatusApproved, child.Status)
		parent, err := f.st.Values().Get(f.ctx, f.revisionID)
		require.NoError(t, err)
		assert.Equal(t, store.ValuesStatusRejected, parent.Status)
	})
}

func TestValuesApproval_AuthenticationAndInputValidation(t *testing.T) {
	t.Run("unauthenticated before resource lookup", func(t *testing.T) {
		f := newApprovalFixture(t)
		req := f.submitRequest("unauthenticated")
		req.Msg.RevisionId = "missing-revision"
		_, err := f.svc.SubmitValuesRevision(f.ctx, req)
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
		assert.NotContains(t, err.Error(), "missing-revision")
	})

	tests := []struct {
		name   string
		mutate func(req *connect.Request[orchestratorv1.SubmitValuesRevisionRequest])
		code   connect.Code
		text   string
	}{
		{name: "missing revision id", mutate: func(req *connect.Request[orchestratorv1.SubmitValuesRevisionRequest]) {
			req.Msg.RevisionId = ""
		}, code: connect.CodeInvalidArgument, text: "revision_id is required"},
		{name: "invalid state version", mutate: func(req *connect.Request[orchestratorv1.SubmitValuesRevisionRequest]) {
			req.Msg.ExpectedStateVersion = 0
		}, code: connect.CodeInvalidArgument, text: "invalid expected_state_version"},
		{name: "oversized comment", mutate: func(req *connect.Request[orchestratorv1.SubmitValuesRevisionRequest]) {
			req.Msg.Comment = strings.Repeat("a", maxApprovalTextBytes+1)
		}, code: connect.CodeInvalidArgument, text: "comment too large"},
		{name: "forbidden comment character", mutate: func(req *connect.Request[orchestratorv1.SubmitValuesRevisionRequest]) {
			req.Msg.Comment = "invalid\x00comment"
		}, code: connect.CodeInvalidArgument, text: "forbidden characters"},
		{name: "oversized idempotency key", mutate: func(req *connect.Request[orchestratorv1.SubmitValuesRevisionRequest]) {
			req.Header().Set("Idempotency-Key", strings.Repeat("k", 65))
		}, code: connect.CodeInvalidArgument, text: "idempotency key too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newApprovalFixture(t)
			req := f.submitRequest("validation")
			tt.mutate(req)
			_, err := f.svc.SubmitValuesRevision(f.actorContext(f.creatorID, store.RoleDeployer), req)
			require.Error(t, err)
			assert.Equal(t, tt.code, connect.CodeOf(err))
			assert.Equal(t, "invalid_argument", approvalReasonCode(t, err))
			assert.Contains(t, err.Error(), tt.text)
		})
	}

	t.Run("reject reason required", func(t *testing.T) {
		f := newApprovalFixture(t)
		f.submit(t)
		_, err := f.svc.RejectValuesRevision(
			f.actorContext(f.adminID, store.RoleReleaseAdmin),
			f.rejectRequest("reason-required", 2, " \t\n "),
		)
		require.Error(t, err)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		assert.Equal(t, "invalid_argument", approvalReasonCode(t, err))
		assert.Contains(t, err.Error(), "reason required")
	})
}

func TestValuesApproval_AuthorizationAndLifecycle(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(t *testing.T, f approvalFixture)
		action     func(f approvalFixture) error
		wantCode   connect.Code
		wantReason string
		wantText   string
	}{
		{
			name:    "AC-068-05 self approval forbidden",
			prepare: func(t *testing.T, f approvalFixture) { f.submit(t) },
			action: func(f approvalFixture) error {
				_, err := f.svc.ApproveValuesRevision(f.actorContext(f.creatorID, store.RoleReleaseAdmin), f.approveRequest("self", 2))
				return err
			},
			wantCode: connect.CodePermissionDenied, wantReason: "self_approval_forbidden", wantText: "creator cannot approve",
		},
		{
			name:    "AC-068-06 viewer cannot submit",
			prepare: func(t *testing.T, f approvalFixture) { setMemberRole(t, f, f.creatorID, store.RoleViewer) },
			action: func(f approvalFixture) error {
				_, err := f.svc.SubmitValuesRevision(f.actorContext(f.creatorID, store.RoleViewer), f.submitRequest("viewer"))
				return err
			},
			wantCode: connect.CodePermissionDenied, wantReason: "role_insufficient", wantText: "insufficient for submit",
		},
		{
			name:    "AC-068-07 deployer cannot approve",
			prepare: func(t *testing.T, f approvalFixture) { f.submit(t); setMemberRole(t, f, f.adminID, store.RoleDeployer) },
			action: func(f approvalFixture) error {
				_, err := f.svc.ApproveValuesRevision(f.actorContext(f.adminID, store.RoleDeployer), f.approveRequest("deployer", 2))
				return err
			},
			wantCode: connect.CodePermissionDenied, wantReason: "role_insufficient", wantText: "insufficient for approve",
		},
		{
			name:    "AC-068-08 cross organization denied",
			prepare: func(t *testing.T, f approvalFixture) { f.submit(t) },
			action: func(f approvalFixture) error {
				ctx := authctx.WithActor(f.ctx, authctx.Actor{UserID: f.adminID, OrganizationID: "other-org", Roles: []string{string(store.RoleReleaseAdmin)}})
				_, err := f.svc.ApproveValuesRevision(ctx, f.approveRequest("cross-org", 2))
				return err
			},
			wantCode: connect.CodePermissionDenied, wantReason: "not_authorized", wantText: "not authorized",
		},
		{
			name: "AC-068-09 disabled customer",
			prepare: func(t *testing.T, f approvalFixture) {
				customer, err := f.st.Customers().Get(f.ctx, f.customerID)
				require.NoError(t, err)
				customer.Status = store.CustomerDisabled
				require.NoError(t, f.st.Customers().Update(f.ctx, customer, customer.Version))
			},
			action: func(f approvalFixture) error {
				_, err := f.svc.SubmitValuesRevision(f.actorContext(f.creatorID, store.RoleDeployer), f.submitRequest("disabled-customer"))
				return err
			},
			wantCode: connect.CodeFailedPrecondition, wantReason: "customer_disabled", wantText: "customer is disabled",
		},
		{
			name: "AC-068-09 disabled organization",
			prepare: func(t *testing.T, f approvalFixture) {
				organization, err := f.st.Organizations().Get(f.ctx, f.orgID)
				require.NoError(t, err)
				organization.Status = store.OrgDisabled
				require.NoError(t, f.st.Organizations().Update(f.ctx, organization))
			},
			action: func(f approvalFixture) error {
				_, err := f.svc.SubmitValuesRevision(f.actorContext(f.creatorID, store.RoleDeployer), f.submitRequest("disabled-organization"))
				return err
			},
			wantCode: connect.CodeFailedPrecondition, wantReason: "organization_disabled", wantText: "organization is disabled",
		},
		{
			name: "AC-068-09 revoked binding",
			prepare: func(t *testing.T, f approvalFixture) {
				binding, err := f.st.Bindings().GetByOrgAndCustomer(f.ctx, f.orgID, f.customerID)
				require.NoError(t, err)
				require.NoError(t, f.st.Bindings().SetStatus(f.ctx, binding.ID, store.BindingRevoked))
			},
			action: func(f approvalFixture) error {
				_, err := f.svc.SubmitValuesRevision(f.actorContext(f.creatorID, store.RoleDeployer), f.submitRequest("revoked-binding"))
				return err
			},
			wantCode: connect.CodeFailedPrecondition, wantReason: "binding_revoked", wantText: "binding is revoked",
		},
		{
			name: "AC-068-09 inactive membership",
			prepare: func(t *testing.T, f approvalFixture) {
				require.NoError(t, f.st.OrgMembers().Delete(f.ctx, f.orgID, f.creatorID))
			},
			action: func(f approvalFixture) error {
				_, err := f.svc.SubmitValuesRevision(f.actorContext(f.creatorID, store.RoleDeployer), f.submitRequest("inactive-membership"))
				return err
			},
			wantCode: connect.CodePermissionDenied, wantReason: "membership_inactive", wantText: "no active membership",
		},
		{
			name: "AC-068-22 disabled definition",
			prepare: func(t *testing.T, f approvalFixture) {
				definition, err := f.st.Definitions().Get(f.ctx, f.defID)
				require.NoError(t, err)
				definition.Status = store.DefStatusDisabled
				_, err = f.st.Definitions().Update(f.ctx, definition, nil)
				require.NoError(t, err)
			},
			action: func(f approvalFixture) error {
				_, err := f.svc.SubmitValuesRevision(f.actorContext(f.creatorID, store.RoleDeployer), f.submitRequest("disabled-def"))
				return err
			},
			wantCode: connect.CodeFailedPrecondition, wantReason: "invalid_revision_state", wantText: "definition",
		},
		{
			name: "AC-068-23 unresolved owner",
			prepare: func(t *testing.T, f approvalFixture) {
				_, err := f.st.DB().ExecContext(f.ctx, `UPDATE release_definitions SET owner_organization_id = NULL WHERE id = ?`, f.defID)
				require.NoError(t, err)
			},
			action: func(f approvalFixture) error {
				_, err := f.svc.SubmitValuesRevision(f.actorContext(f.creatorID, store.RoleDeployer), f.submitRequest("owner-null"))
				return err
			},
			wantCode: connect.CodeFailedPrecondition, wantReason: "release_definition_owner_unresolved", wantText: "owner organization must be set",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newApprovalFixture(t)
			tt.prepare(t, f)
			beforeRevision, err := f.st.Values().Get(f.ctx, f.revisionID)
			require.NoError(t, err)
			beforeDecisions, err := f.st.ValuesApprovalEvidence().ListDecisions(f.ctx, f.revisionID)
			require.NoError(t, err)
			beforeAudit := approvalAuditEntries(t, f)
			beforeNotifications := approvalNotificationEntries(t, f)

			err = tt.action(f)
			require.Error(t, err)
			assert.Equal(t, tt.wantCode, connect.CodeOf(err))
			assert.Equal(t, tt.wantReason, approvalReasonCode(t, err))
			assert.Contains(t, err.Error(), tt.wantText)

			afterRevision, getErr := f.st.Values().Get(f.ctx, f.revisionID)
			require.NoError(t, getErr)
			assert.Equal(t, beforeRevision.Status, afterRevision.Status)
			assert.Equal(t, beforeRevision.StateVersion, afterRevision.StateVersion)
			afterDecisions, listErr := f.st.ValuesApprovalEvidence().ListDecisions(f.ctx, f.revisionID)
			require.NoError(t, listErr)
			assert.Len(t, afterDecisions, len(beforeDecisions))
			assert.Len(t, approvalNotificationEntries(t, f), len(beforeNotifications))
			afterAudit := approvalAuditEntries(t, f)
			require.Len(t, afterAudit, len(beforeAudit)+1)
			var attempt map[string]any
			require.NoError(t, json.Unmarshal(afterAudit[len(afterAudit)-1].PayloadJSON, &attempt))
			assert.Equal(t, tt.wantReason, attempt["reason_code"])
		})
	}
}

func TestValuesApproval_IdempotencyConcurrencyAndState(t *testing.T) {
	t.Run("AC-068-10 replay returns first result without duplicates", func(t *testing.T) {
		f := newApprovalFixture(t)
		ctx := f.actorContext(f.creatorID, store.RoleDeployer)
		first, err := f.svc.SubmitValuesRevision(ctx, f.submitRequest("replay"))
		require.NoError(t, err)
		second, err := f.svc.SubmitValuesRevision(ctx, f.submitRequest("replay"))
		require.NoError(t, err)
		assert.True(t, proto.Equal(first.Msg, second.Msg))
		decisions, err := f.st.ValuesApprovalEvidence().ListDecisions(f.ctx, f.revisionID)
		require.NoError(t, err)
		assert.Len(t, decisions, 1)
		assert.Len(t, approvalAuditEntries(t, f), 1)
		assert.Len(t, approvalNotificationEntries(t, f), 1)
	})

	t.Run("AC-068-11 same key different payload conflicts", func(t *testing.T) {
		f := newApprovalFixture(t)
		ctx := f.actorContext(f.creatorID, store.RoleDeployer)
		_, err := f.svc.SubmitValuesRevision(ctx, f.submitRequest("conflict"))
		require.NoError(t, err)
		conflict := f.submitRequest("conflict")
		conflict.Msg.Comment = "different"
		_, err = f.svc.SubmitValuesRevision(ctx, conflict)
		require.Error(t, err)
		assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
		assert.Equal(t, "idempotency_conflict", approvalReasonCode(t, err))
	})

	for _, tc := range []struct {
		name         string
		secondReject bool
	}{
		{name: "AC-068-12 concurrent approve"},
		{name: "AC-068-13 concurrent approve and reject", secondReject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newApprovalFixture(t)
			f.submit(t)
			secondID := "admin-068-second"
			require.NoError(t, f.st.Users().Create(f.ctx, &store.User{ID: secondID, Username: secondID, PasswordHash: "unused"}))
			require.NoError(t, f.st.OrgMembers().Create(f.ctx, &store.OrganizationMember{OrgID: f.orgID, UserID: secondID, Role: store.RoleReleaseAdmin}))

			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, err := f.svc.ApproveValuesRevision(f.actorContext(f.adminID, store.RoleReleaseAdmin), f.approveRequest("race-a", 2))
				results <- err
			}()
			go func() {
				defer wg.Done()
				<-start
				if tc.secondReject {
					_, err := f.svc.RejectValuesRevision(f.actorContext(secondID, store.RoleReleaseAdmin), f.rejectRequest("race-b", 2, "reject"))
					results <- err
					return
				}
				_, err := f.svc.ApproveValuesRevision(f.actorContext(secondID, store.RoleReleaseAdmin), f.approveRequest("race-b", 2))
				results <- err
			}()
			close(start)
			wg.Wait()
			close(results)
			var success, aborted int
			for err := range results {
				if err == nil {
					success++
					continue
				}
				require.Equal(t, connect.CodeAborted, connect.CodeOf(err))
				assert.Equal(t, "optimistic_lock_conflict", approvalReasonCode(t, err))
				aborted++
			}
			assert.Equal(t, 1, success)
			assert.Equal(t, 1, aborted)
			auditEntries := approvalAuditEntries(t, f)
			assert.Len(t, auditEntries, 3, "submit, winning decision, and rejected attempt are required")
			assert.Len(t, approvalNotificationEntries(t, f), 2)
			decisions, err := f.st.ValuesApprovalEvidence().ListDecisions(f.ctx, f.revisionID)
			require.NoError(t, err)
			assert.Len(t, decisions, 2)
		})
	}

	t.Run("AC-068-14 only one pending per definition", func(t *testing.T) {
		f := newApprovalFixture(t)
		f.submit(t)
		otherID := "revision-068-other"
		require.NoError(t, f.st.Values().Create(f.ctx, &store.ValuesRevision{
			ID: otherID, ReleaseDefinitionID: f.defID, Version: 2, StateVersion: 1,
			Status: store.ValuesStatusDraft, CanonicalDocument: []byte(`{}`), Digest: "sha256:other",
			ParentRevisionID: f.revisionID, CreatedByUserID: f.creatorID,
		}))
		req := connect.NewRequest(&orchestratorv1.SubmitValuesRevisionRequest{RevisionId: otherID, ExpectedStateVersion: 1})
		req.Header().Set("Idempotency-Key", "second-pending")
		_, err := f.svc.SubmitValuesRevision(f.actorContext(f.creatorID, store.RoleDeployer), req)
		require.Error(t, err)
		assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		assert.Equal(t, "approval_already_pending", approvalReasonCode(t, err))
	})

	t.Run("AC-068-16 and AC-068-17 success and failure outbox boundaries", func(t *testing.T) {
		f := newApprovalFixture(t)
		f.submit(t)
		_, err := f.svc.ApproveValuesRevision(
			f.actorContext(f.creatorID, store.RoleReleaseAdmin), f.approveRequest("failed-self-audit", 2),
		)
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		assert.Equal(t, "self_approval_forbidden", approvalReasonCode(t, err))
		auditEntries := approvalAuditEntries(t, f)
		require.Len(t, auditEntries, 2)
		assert.Len(t, approvalNotificationEntries(t, f), 1)
		var failurePayload map[string]any
		require.NoError(t, json.Unmarshal(auditEntries[1].PayloadJSON, &failurePayload))
		assert.NotEmpty(t, failurePayload["request_id"])
		assert.Equal(t, "self_approval_forbidden", failurePayload["reason_code"])
		decisions, err := f.st.ValuesApprovalEvidence().ListDecisions(f.ctx, f.revisionID)
		require.NoError(t, err)
		assert.Len(t, decisions, 1)
	})
	t.Run("AC-068-15 all historical approved revisions are superseded", func(t *testing.T) {
		f := newApprovalFixture(t)
		_, err := f.st.DB().ExecContext(f.ctx, `DROP INDEX ux_vr_one_approved_per_def`)
		require.NoError(t, err)
		parentID := f.revisionID
		for i := range 2 {
			version := int64(i + 2)
			id := fmt.Sprintf("historical-%d", i)
			require.NoError(t, f.st.Values().Create(f.ctx, &store.ValuesRevision{
				ID: id, ReleaseDefinitionID: f.defID, Version: version,
				StateVersion: 1, Status: store.ValuesStatusApproved, CanonicalDocument: []byte(`{}`),
				Digest: fmt.Sprintf("sha256:%d", i), ParentRevisionID: parentID, CreatedByUserID: "historical",
			}))
			parentID = id
		}
		f.submit(t)
		resp, err := f.svc.ApproveValuesRevision(f.actorContext(f.adminID, store.RoleReleaseAdmin), f.approveRequest("multi-old", 2))
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"historical-0", "historical-1"}, resp.Msg.GetSupersededRevisionIds())
	})
	for _, tc := range []struct {
		name   string
		status store.ValuesStatus
		action func(f approvalFixture) error
	}{
		{name: "AC-068-19 draft direct approve", status: store.ValuesStatusDraft, action: func(f approvalFixture) error {
			_, err := f.svc.ApproveValuesRevision(f.actorContext(f.adminID, store.RoleReleaseAdmin), f.approveRequest("draft-approve", 1))
			return err
		}},
		{name: "AC-068-20 approved repeated approve", status: store.ValuesStatusApproved, action: func(f approvalFixture) error {
			_, err := f.svc.ApproveValuesRevision(f.actorContext(f.adminID, store.RoleReleaseAdmin), f.approveRequest("approved-repeat", 1))
			return err
		}},
		{name: "AC-068-21 rejected repeated reject", status: store.ValuesStatusRejected, action: func(f approvalFixture) error {
			_, err := f.svc.RejectValuesRevision(f.actorContext(f.adminID, store.RoleReleaseAdmin), f.rejectRequest("rejected-repeat", 1, "again"))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newApprovalFixture(t)
			_, err := f.st.DB().ExecContext(f.ctx, `UPDATE values_revisions SET status = ? WHERE id = ?`, string(tc.status), f.revisionID)
			require.NoError(t, err)
			err = tc.action(f)
			require.Error(t, err)
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			assert.Equal(t, "invalid_revision_state", approvalReasonCode(t, err))
			auditEntries := approvalAuditEntries(t, f)
			require.Len(t, auditEntries, 1)
			var attempt map[string]any
			require.NoError(t, json.Unmarshal(auditEntries[0].PayloadJSON, &attempt))
			assert.Equal(t, "invalid_revision_state", attempt["reason_code"])
			assert.Empty(t, approvalNotificationEntries(t, f))
			decisions, listErr := f.st.ValuesApprovalEvidence().ListDecisions(f.ctx, f.revisionID)
			require.NoError(t, listErr)
			assert.Empty(t, decisions)
		})
	}
}

func TestValuesApproval_RollbackSemanticsAndUnavailable(t *testing.T) {
	t.Run("AC-068-18 transaction rollback leaves no partial rows", func(t *testing.T) {
		f := newApprovalFixture(t)
		_, err := f.st.DB().ExecContext(f.ctx, `DROP TABLE notification_outbox`)
		require.NoError(t, err)
		_, err = f.svc.SubmitValuesRevision(f.actorContext(f.creatorID, store.RoleDeployer), f.submitRequest("rollback"))
		require.Error(t, err)
		assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
		assert.Equal(t, "internal_error", approvalReasonCode(t, err))
		revision, getErr := f.st.Values().Get(f.ctx, f.revisionID)
		require.NoError(t, getErr)
		assert.Equal(t, store.ValuesStatusDraft, revision.Status)
		assert.EqualValues(t, 1, revision.StateVersion)
		decisions, listErr := f.st.ValuesApprovalEvidence().ListDecisions(f.ctx, f.revisionID)
		require.NoError(t, listErr)
		assert.Empty(t, decisions)
		assert.Empty(t, approvalAuditEntries(t, f))
		var idempotencyCount int
		require.NoError(t, f.st.DB().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM idempotency_records`).Scan(&idempotencyCount))
		assert.Zero(t, idempotencyCount)
	})

	t.Run("AC-068-24 and AC-068-25 corrected approval replaces pointer", func(t *testing.T) {
		f := newApprovalFixture(t)
		f.submit(t)
		_, err := f.svc.ApproveValuesRevision(f.actorContext(f.adminID, store.RoleReleaseAdmin), f.approveRequest("wrong-approval", 2))
		require.NoError(t, err)
		correctedID := "corrected-068"
		require.NoError(t, f.st.Values().Create(f.ctx, &store.ValuesRevision{
			ID: correctedID, ReleaseDefinitionID: f.defID, Version: 2, StateVersion: 1,
			Status: store.ValuesStatusDraft, CanonicalDocument: []byte(`{"replicas":2}`), Digest: "sha256:corrected",
			ParentRevisionID: f.revisionID, CreatedByUserID: f.creatorID,
		}))
		submit := connect.NewRequest(&orchestratorv1.SubmitValuesRevisionRequest{RevisionId: correctedID, ExpectedStateVersion: 1})
		submit.Header().Set("Idempotency-Key", "corrected-submit")
		_, err = f.svc.SubmitValuesRevision(f.actorContext(f.creatorID, store.RoleDeployer), submit)
		require.NoError(t, err)
		approve := connect.NewRequest(&orchestratorv1.ApproveValuesRevisionRequest{RevisionId: correctedID, ExpectedStateVersion: 2})
		approve.Header().Set("Idempotency-Key", "corrected-approve")
		_, err = f.svc.ApproveValuesRevision(f.actorContext(f.adminID, store.RoleReleaseAdmin), approve)
		require.NoError(t, err)
		wrong, err := f.st.Values().Get(f.ctx, f.revisionID)
		require.NoError(t, err)
		assert.Equal(t, store.ValuesStatusSuperseded, wrong.Status)
		definition, err := f.st.Definitions().Get(f.ctx, f.defID)
		require.NoError(t, err)
		require.NotNil(t, definition.ApprovedRevisionID)
		assert.Equal(t, correctedID, *definition.ApprovedRevisionID)
	})

	t.Run("AC-068-26 unavailable authorization dependency changes nothing", func(t *testing.T) {
		f := newApprovalFixture(t)
		_, err := f.st.DB().ExecContext(f.ctx, `DROP TABLE organization_members`)
		require.NoError(t, err)
		_, err = f.svc.SubmitValuesRevision(f.actorContext(f.creatorID, store.RoleDeployer), f.submitRequest("auth-unavailable"))
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
		assert.Equal(t, "dependency_unavailable", approvalReasonCode(t, err))
		assert.Equal(t, "unavailable: authorization service unavailable", err.Error())
		revision, getErr := f.st.Values().Get(f.ctx, f.revisionID)
		require.NoError(t, getErr)
		assert.Equal(t, store.ValuesStatusDraft, revision.Status)
		assert.EqualValues(t, 1, revision.StateVersion)
		decisions, listErr := f.st.ValuesApprovalEvidence().ListDecisions(f.ctx, f.revisionID)
		require.NoError(t, listErr)
		assert.Empty(t, decisions)
		assert.Empty(t, approvalAuditEntries(t, f))
		assert.Empty(t, approvalNotificationEntries(t, f))
	})
}

func approvalReasonCode(t *testing.T, err error) string {
	t.Helper()
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	return connectErr.Meta().Get("X-Reason-Code")
}

func setMemberRole(t *testing.T, f approvalFixture, userID string, role store.Role) {
	t.Helper()
	member, err := f.st.OrgMembers().Get(f.ctx, f.orgID, userID)
	require.NoError(t, err)
	member.Role = role
	require.NoError(t, f.st.OrgMembers().Update(f.ctx, member))
}

func approvalAuditEntries(t *testing.T, f approvalFixture) []*store.ApprovalOutboxEntry {
	t.Helper()
	entries, err := f.st.ValuesApprovalEvidence().ListAuditOutbox(f.ctx, f.revisionID)
	require.NoError(t, err)
	return entries
}

func approvalNotificationEntries(t *testing.T, f approvalFixture) []*store.ApprovalOutboxEntry {
	t.Helper()
	entries, err := f.st.ValuesApprovalEvidence().ListNotificationOutbox(f.ctx, f.revisionID)
	require.NoError(t, err)
	return entries
}
