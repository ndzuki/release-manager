package orchestrator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/auth"
	"github.com/ndzuki/release-manager/internal/store"
)

type valuesFixture struct {
	svc        *Service
	store      store.Store
	orgID      string
	definition *store.ReleaseDefinition
	creatorID  string
	approverID string
	viewerID   string
	parent     *store.ValuesRevision
}

func newValuesFixture(t *testing.T) valuesFixture {
	t.Helper()
	svc, st, cleanup := setupService(t)
	t.Cleanup(cleanup)
	const (
		customerID = "customer-values"
		clusterID  = "cluster-values"
		orgID      = "org-values"
		creatorID  = "creator-values"
		approverID = "approver-values"
		viewerID   = "viewer-values"
	)
	ctx := context.Background()
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: customerID, Name: "Values Customer", Slug: "values-customer"}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: clusterID, Name: "Values Cluster", CustomerID: customerID}))
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "Values Organization"}))
	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{ID: "binding-values", OrgID: orgID, CustomerID: customerID}))
	for userID, role := range map[string]store.Role{creatorID: store.RoleDeployer, approverID: store.RoleReleaseAdmin, viewerID: store.RoleViewer} {
		require.NoError(t, st.Users().Create(ctx, &store.User{ID: userID, Username: userID, PasswordHash: "hash"}))
		require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: orgID, UserID: userID, Role: role}))
	}
	definition := &store.ReleaseDefinition{
		ID: "definition-values", Name: "values-app", CustomerID: customerID, ClusterID: clusterID,
		Namespace: "apps", ReleaseName: "values-app", ChartName: "values-chart", Status: store.DefStatusActive,
		CreatedBy: creatorID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, st.Definitions().Create(ctx, definition, nil))
	parent := &store.ValuesRevision{
		ID: "parent-values", ReleaseDefinitionID: definition.ID, Revision: 1, Version: 3,
		Status: store.ValuesStatusApproved, Values: []byte(`{"replicas":1}`), Digest: "sha256:parent-values",
		CreatedBy: approverID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, st.Values().Create(ctx, parent))
	return valuesFixture{svc: svc, store: st, orgID: orgID, definition: definition, creatorID: creatorID, approverID: approverID, viewerID: viewerID, parent: parent}
}

func (f valuesFixture) actorContext(userID string, roles ...store.Role) context.Context {
	roleNames := make([]string, 0, len(roles))
	for _, role := range roles {
		roleNames = append(roleNames, string(role))
	}
	return auth.WithAuthorizationContext(context.Background(), userID, f.orgID, roleNames)
}

func reasonCode(t *testing.T, err error) string {
	t.Helper()
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	return connectErr.Meta().Get("X-Reason-Code")
}

func TestValuesRevisionCreateRejectsSecretLiteralAndInvalidSecretRef(t *testing.T) {
	fixture := newValuesFixture(t)
	ctx := fixture.actorContext(fixture.creatorID, store.RoleDeployer)

	_, err := fixture.svc.CreateValuesRevision(ctx, connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId:   fixture.definition.ID,
		ParentRevisionId:      fixture.parent.ID,
		ExpectedParentVersion: int32(fixture.parent.Version),
		Document:              []byte("password: my-secret-value"),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Equal(t, "secret_literal_forbidden", reasonCode(t, err))

	_, err = fixture.svc.CreateValuesRevision(ctx, connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId:   fixture.definition.ID,
		ParentRevisionId:      fixture.parent.ID,
		ExpectedParentVersion: int32(fixture.parent.Version),
		Document:              []byte("replicas: 2"),
		SecretRefs:            []*commonv1.SecretRef{{Name: "database"}},
	}))
	require.Error(t, err)
	assert.Equal(t, "invalid_secret_ref", reasonCode(t, err))
}

func TestValuesRevisionConcurrentCreateOnlyOneDraft(t *testing.T) {
	fixture := newValuesFixture(t)
	ctx := fixture.actorContext(fixture.creatorID, store.RoleDeployer)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := fixture.svc.CreateValuesRevision(ctx, connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
				ReleaseDefinitionId:   fixture.definition.ID,
				ParentRevisionId:      fixture.parent.ID,
				ExpectedParentVersion: int32(fixture.parent.Version),
				Document:              []byte("replicas: 2"),
			}))
			results <- err
		}()
	}

	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case connect.CodeOf(err) == connect.CodeFailedPrecondition && reasonCode(t, err) == "parent_conflict":
			conflicts++
		default:
			t.Fatalf("unexpected create result: %v", err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, conflicts)
}

func TestValuesRevisionApprovalAuthorizationAndCAS(t *testing.T) {
	fixture := newValuesFixture(t)
	creatorCtx := fixture.actorContext(fixture.creatorID, store.RoleReleaseAdmin)
	created, err := fixture.svc.CreateValuesRevision(creatorCtx, connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId:   fixture.definition.ID,
		ParentRevisionId:      fixture.parent.ID,
		ExpectedParentVersion: int32(fixture.parent.Version),
		Document:              []byte("replicas: 2"),
	}))
	require.NoError(t, err)
	draft := created.Msg.GetRevision()

	_, err = fixture.svc.ApproveValuesRevision(creatorCtx, connect.NewRequest(&orchestratorv1.ApproveValuesRevisionRequest{RevisionId: draft.GetId(), ExpectedVersion: draft.GetVersion()}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Equal(t, "permission_denied", reasonCode(t, err))

	viewerCtx := fixture.actorContext(fixture.viewerID, store.RoleViewer)
	_, err = fixture.svc.ApproveValuesRevision(viewerCtx, connect.NewRequest(&orchestratorv1.ApproveValuesRevisionRequest{RevisionId: draft.GetId(), ExpectedVersion: draft.GetVersion()}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	approverCtx := fixture.actorContext(fixture.approverID, store.RoleReleaseAdmin)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, approveErr := fixture.svc.ApproveValuesRevision(approverCtx, connect.NewRequest(&orchestratorv1.ApproveValuesRevisionRequest{RevisionId: draft.GetId(), ExpectedVersion: draft.GetVersion()}))
			results <- approveErr
		}()
	}
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case connect.CodeOf(err) == connect.CodeFailedPrecondition && reasonCode(t, err) == "not_approved":
			conflicts++
		default:
			t.Fatalf("unexpected approve result: %v", err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, conflicts)
}

func TestValuesRevisionRejectPersistsReasonAndTimestamp(t *testing.T) {
	fixture := newValuesFixture(t)
	created, err := fixture.svc.CreateValuesRevision(fixture.actorContext(fixture.creatorID, store.RoleDeployer), connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId:   fixture.definition.ID,
		ParentRevisionId:      fixture.parent.ID,
		ExpectedParentVersion: int32(fixture.parent.Version),
		Document:              []byte("replicas: 2"),
		SecretRefs:            []*commonv1.SecretRef{{Path: ".secrets.database.password", Name: "database", Key: "password"}},
	}))
	require.NoError(t, err)

	rejected, err := fixture.svc.RejectValuesRevision(fixture.actorContext(fixture.approverID, store.RoleReleaseAdmin), connect.NewRequest(&orchestratorv1.RejectValuesRevisionRequest{
		RevisionId: created.Msg.GetRevision().GetId(), ExpectedVersion: created.Msg.GetRevision().GetVersion(), Reason: "use the production replica count",
	}))
	require.NoError(t, err)
	assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_REJECTED, rejected.Msg.GetRevision().GetStatus())
	assert.Equal(t, "use the production replica count", rejected.Msg.GetRevision().GetReason())
	assert.NotNil(t, rejected.Msg.GetRevision().GetRejectedAt())

	stored, err := fixture.store.Values().Get(context.Background(), created.Msg.GetRevision().GetId())
	require.NoError(t, err)
	assert.Equal(t, "use the production replica count", stored.RejectionReason)
	assert.NotNil(t, stored.RejectedAt)
	var refs []*commonv1.SecretRef
	require.NoError(t, json.Unmarshal(stored.SecretRefs, &refs))
	assert.Equal(t, ".secrets.database.password", refs[0].GetPath())
}
