package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	authctx "github.com/ndzuki/release-manager/internal/authctx"
	"github.com/ndzuki/release-manager/internal/authorization"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

type valuesRevisionFixture struct {
	t          *testing.T
	svc        *Service
	st         *sqlitestore.Store
	ctx        context.Context
	orgID      string
	customerID string
	defID      string
	creatorID  string
	adminID    string
	actor      authctx.Actor
}

func newValuesRevisionFixture(t *testing.T) valuesRevisionFixture {
	t.Helper()
	st := sqlitestore.OpenTest(t)
	ctx := context.Background()
	orgID := "org-vr"
	customerID := "customer-vr"
	defID := "definition-vr"
	creatorID := "creator-vr"
	adminID := "admin-vr"
	owner := orgID

	require.NoError(t, st.Customers().Create(ctx, &store.Customer{
		ID: customerID, Name: "Customer VR", Slug: "customer-vr",
	}))
	require.NoError(t, st.Organizations().Create(ctx, &store.Organization{ID: orgID, Name: "Organization VR"}))
	require.NoError(t, st.Bindings().Create(ctx, &store.OrgCustomerBinding{
		ID: "binding-vr", OrgID: orgID, CustomerID: customerID,
	}))
	for userID, role := range map[string]store.Role{
		creatorID: store.RoleDeployer,
		adminID:   store.RoleReleaseAdmin,
	} {
		require.NoError(t, st.Users().Create(ctx, &store.User{ID: userID, Username: userID, PasswordHash: "unused"}))
		require.NoError(t, st.OrgMembers().Create(ctx, &store.OrganizationMember{OrgID: orgID, UserID: userID, Role: role}))
	}
	require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
		ID: defID, Name: "definition-vr", CustomerID: customerID, ClusterID: "cluster-vr",
		ReleaseName: "release-vr", Status: store.DefStatusActive, OwnerOrganizationID: &owner,
	}, nil))

	actor := authctx.Actor{UserID: creatorID, OrganizationID: orgID, Roles: []string{string(store.RoleDeployer)}}
	authCtx := authctx.WithActor(ctx, actor)

	return valuesRevisionFixture{
		t:          t,
		svc:        NewService(st, nil, "staging", nil, authorization.NewStoreAuthorizer(st), slog.New(slog.DiscardHandler)),
		st:         st,
		ctx:        authCtx,
		orgID:      orgID,
		customerID: customerID,
		defID:      defID,
		creatorID:  creatorID,
		adminID:    adminID,
		actor:      actor,
	}
}

func (f valuesRevisionFixture) adminContext() context.Context {
	return authctx.WithActor(f.ctx, authctx.Actor{
		UserID: f.adminID, OrganizationID: f.orgID, Roles: []string{string(store.RoleReleaseAdmin)},
	})
}

func TestCreateValuesRevision_Success(t *testing.T) {
	f := newValuesRevisionFixture(t)

	req := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 3`,
	})
	req.Header().Set("Idempotency-Key", "create-1")

	resp, err := f.svc.CreateValuesRevision(f.ctx, req)
	require.NoError(t, err)
	assert.True(t, resp.Msg.Created)
	assert.NotEmpty(t, resp.Msg.Revision.Id)
	assert.Equal(t, f.defID, resp.Msg.Revision.ReleaseDefinitionId)
	assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_DRAFT, resp.Msg.Revision.Status)
	assert.NotEmpty(t, resp.Msg.Revision.Digest)
	assert.Equal(t, int64(1), resp.Msg.Revision.Version)
}

func TestCreateValuesRevision_InitialCanBeApprovedAndUsedForInstall(t *testing.T) {
	f := newValuesRevisionFixture(t)
	createRequest := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 3`,
	})
	createRequest.Header().Set("Idempotency-Key", "initial-install-create")
	created, err := f.svc.CreateValuesRevision(f.ctx, createRequest)
	require.NoError(t, err)

	submitRequest := connect.NewRequest(&orchestratorv1.SubmitValuesRevisionRequest{
		RevisionId: created.Msg.Revision.Id, ExpectedStateVersion: created.Msg.Revision.StateVersion,
	})
	submitRequest.Header().Set("Idempotency-Key", "initial-install-submit")
	submitted, err := f.svc.SubmitValuesRevision(f.ctx, submitRequest)
	require.NoError(t, err)

	approveRequest := connect.NewRequest(&orchestratorv1.ApproveValuesRevisionRequest{
		RevisionId: created.Msg.Revision.Id, ExpectedStateVersion: submitted.Msg.Revision.StateVersion,
	})
	approveRequest.Header().Set("Idempotency-Key", "initial-install-approve")
	approved, err := f.svc.ApproveValuesRevision(
		f.adminContext(), approveRequest,
	)
	require.NoError(t, err)

	require.NoError(t, f.st.Bundles().Create(f.ctx, &store.ReleaseBundle{
		ID: "bundle-initial-install", Name: "Initial Install Bundle",
		DigestAlg: "sha256", DigestValue: fmt.Sprintf("%064x", 42), Status: store.BundleValidated,
		CreatedAt: time.Now().UTC(),
	}))

	operationRequest := connect.NewRequest(&orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-initial-install",
		ReleaseDefinitionId: f.defID,
		ValuesRevisionId:    approved.Msg.Revision.Id,
		IdempotencyKey:      "initial-install-operation",
		Actor: &commonv1.ActorContext{
			UserId: f.creatorID, Organization: f.orgID,
		},
	})
	operation, err := f.svc.CreateOperation(f.ctx, operationRequest)
	require.NoError(t, err)
	assert.NotEmpty(t, operation.Msg.OperationId)
	assert.Equal(t, "preflight", operation.Msg.State)
}

func TestCreateValuesRevision_Idempotent(t *testing.T) {
	f := newValuesRevisionFixture(t)

	req := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 3`,
	})
	req.Header().Set("Idempotency-Key", "create-idem-1")

	resp1, err := f.svc.CreateValuesRevision(f.ctx, req)
	require.NoError(t, err)
	assert.True(t, resp1.Msg.Created)

	resp2, err := f.svc.CreateValuesRevision(f.ctx, req)
	require.NoError(t, err)
	assert.False(t, resp2.Msg.Created)
	assert.Equal(t, resp1.Msg.Revision.Id, resp2.Msg.Revision.Id)
}

func TestCreateValuesRevision_InvalidYAML(t *testing.T) {
	f := newValuesRevisionFixture(t)

	req := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `{invalid`,
	})
	req.Header().Set("Idempotency-Key", "create-bad-1")

	_, err := f.svc.CreateValuesRevision(f.ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestCreateValuesRevision_SecretLiteralForbidden(t *testing.T) {
	f := newValuesRevisionFixture(t)

	req := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `password: "my-secret-value-123"`,
	})
	req.Header().Set("Idempotency-Key", "create-secret-1")

	_, err := f.svc.CreateValuesRevision(f.ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "secret_literal_forbidden")
}

func TestCreateValuesRevision_NoAuth(t *testing.T) {
	f := newValuesRevisionFixture(t)

	req := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 1`,
	})
	req.Header().Set("Idempotency-Key", "create-noauth-1")

	_, err := f.svc.CreateValuesRevision(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestCreateValuesRevision_DefinitionNotFound(t *testing.T) {
	f := newValuesRevisionFixture(t)

	req := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: "nonexistent",
		Document:            `replicas: 1`,
	})
	req.Header().Set("Idempotency-Key", "create-nodef-1")

	_, err := f.svc.CreateValuesRevision(f.ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestCreateValuesRevision_MissingDocument(t *testing.T) {
	f := newValuesRevisionFixture(t)

	req := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            "",
	})
	req.Header().Set("Idempotency-Key", "create-empty-1")

	_, err := f.svc.CreateValuesRevision(f.ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestCreateValuesRevision_MissingDefinitionID(t *testing.T) {
	f := newValuesRevisionFixture(t)

	req := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: "",
		Document:            `replicas: 1`,
	})
	req.Header().Set("Idempotency-Key", "create-nodefid-1")

	_, err := f.svc.CreateValuesRevision(f.ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestCreateValuesRevision_WithParent(t *testing.T) {
	f := newValuesRevisionFixture(t)

	req1 := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 1`,
	})
	req1.Header().Set("Idempotency-Key", "create-parent-1")
	resp1, err := f.svc.CreateValuesRevision(f.ctx, req1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), resp1.Msg.Revision.Version)

	req2 := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId:   f.defID,
		Document:              `replicas: 2`,
		ParentRevisionId:      resp1.Msg.Revision.Id,
		ExpectedParentVersion: resp1.Msg.Revision.Version,
	})
	req2.Header().Set("Idempotency-Key", "create-parent-2")
	resp2, err := f.svc.CreateValuesRevision(f.ctx, req2)
	require.NoError(t, err)
	assert.Equal(t, int64(2), resp2.Msg.Revision.Version)
	assert.Equal(t, resp1.Msg.Revision.Id, resp2.Msg.Revision.ParentRevisionId)
}

func TestCreateValuesRevision_RequiresParentAfterInitial(t *testing.T) {
	f := newValuesRevisionFixture(t)
	first := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 1`,
	})
	first.Header().Set("Idempotency-Key", "create-requires-parent-1")
	_, err := f.svc.CreateValuesRevision(f.ctx, first)
	require.NoError(t, err)

	second := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 2`,
	})
	second.Header().Set("Idempotency-Key", "create-requires-parent-2")
	_, err = f.svc.CreateValuesRevision(f.ctx, second)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, "invalid_argument", connectErr.Meta().Get("X-Reason-Code"))
}

func TestCreateValuesRevision_IdempotencyConflict(t *testing.T) {
	f := newValuesRevisionFixture(t)
	first := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 1`,
	})
	first.Header().Set("Idempotency-Key", "create-idempotency-conflict")
	firstResponse, err := f.svc.CreateValuesRevision(f.ctx, first)
	require.NoError(t, err)

	second := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId:   f.defID,
		Document:              `replicas: 2`,
		ParentRevisionId:      firstResponse.Msg.Revision.Id,
		ExpectedParentVersion: firstResponse.Msg.Revision.Version,
	})
	second.Header().Set("Idempotency-Key", "create-idempotency-conflict")
	_, err = f.svc.CreateValuesRevision(f.ctx, second)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

func TestCreateValuesRevision_ValidatesSizeAndSecretRefs(t *testing.T) {
	f := newValuesRevisionFixture(t)
	f.svc.valuesConfig = ValuesConfig{MaxDocumentBytes: 8}
	sizeRequest := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 123`,
	})
	sizeRequest.Header().Set("Idempotency-Key", "create-size-exceeded")
	_, err := f.svc.CreateValuesRevision(f.ctx, sizeRequest)
	require.Error(t, err)
	assert.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))

	f.svc.valuesConfig = DefaultValuesConfig()
	refRequest := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `database: {password: null}`,
		SecretRefs: []*commonv1.SecretRef{{
			Path: "/database/missing",
			Name: "database",
			Key:  "password",
		}},
	})
	refRequest.Header().Set("Idempotency-Key", "create-invalid-ref")
	_, err = f.svc.CreateValuesRevision(f.ctx, refRequest)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "invalid_secret_ref")
}

func TestCreateValuesRevision_RejectsAnchorAndMultiDocument(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		document string
	}{
		{name: "anchor", document: "defaults: &defaults\n  replicas: 1\napp: *defaults"},
		{name: "multiple documents", document: "replicas: 1\n---\nreplicas: 2"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			f := newValuesRevisionFixture(t)
			request := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
				ReleaseDefinitionId: f.defID,
				Document:            testCase.document,
			})
			request.Header().Set("Idempotency-Key", "create-invalid-yaml-"+testCase.name)
			_, err := f.svc.CreateValuesRevision(f.ctx, request)
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			assert.Contains(t, err.Error(), "invalid_yaml")
		})
	}
}

func TestCreateValuesRevision_RejectsStaleAuthorizationWithoutWrite(t *testing.T) {
	f := newValuesRevisionFixture(t)
	f.svc.authorizer = &advancingAuthorizer{
		delegate: authorization.NewStoreAuthorizer(f.st),
		store:    f.st,
	}
	request := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 1`,
	})
	request.Header().Set("Idempotency-Key", "create-stale-auth")
	_, err := f.svc.CreateValuesRevision(f.ctx, request)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	items, listErr := f.st.Values().List(f.ctx, f.defID)
	require.NoError(t, listErr)
	assert.Empty(t, items)
}

func TestGetValuesRevision_Success(t *testing.T) {
	f := newValuesRevisionFixture(t)

	createReq := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 5`,
	})
	createReq.Header().Set("Idempotency-Key", "get-test-1")
	createResp, err := f.svc.CreateValuesRevision(f.ctx, createReq)
	require.NoError(t, err)

	getReq := connect.NewRequest(&orchestratorv1.GetValuesRevisionRequest{
		RevisionId: createResp.Msg.Revision.Id,
	})
	getResp, err := f.svc.GetValuesRevision(f.ctx, getReq)
	require.NoError(t, err)
	assert.Equal(t, createResp.Msg.Revision.Id, getResp.Msg.Id)
	assert.Equal(t, createResp.Msg.Revision.Digest, getResp.Msg.Digest)
}

func TestGetValuesRevision_NotFound(t *testing.T) {
	f := newValuesRevisionFixture(t)

	req := connect.NewRequest(&orchestratorv1.GetValuesRevisionRequest{
		RevisionId: "nonexistent",
	})
	_, err := f.svc.GetValuesRevision(f.ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestListValuesRevisions_Success(t *testing.T) {
	f := newValuesRevisionFixture(t)

	createReq1 := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 1`,
	})
	createReq1.Header().Set("Idempotency-Key", "list-test-1")
	resp1, err := f.svc.CreateValuesRevision(f.ctx, createReq1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), resp1.Msg.Revision.Version)

	createReq2 := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId:   f.defID,
		Document:              `replicas: 2`,
		ParentRevisionId:      resp1.Msg.Revision.Id,
		ExpectedParentVersion: resp1.Msg.Revision.Version,
	})
	createReq2.Header().Set("Idempotency-Key", "list-test-2")
	_, err = f.svc.CreateValuesRevision(f.ctx, createReq2)
	require.NoError(t, err)

	listReq := connect.NewRequest(&orchestratorv1.ListValuesRevisionsRequest{
		ReleaseDefinitionId: f.defID,
		PageSize:            10,
	})
	listResp, err := f.svc.ListValuesRevisions(f.ctx, listReq)
	require.NoError(t, err)
	assert.Len(t, listResp.Msg.Items, 2)
	assert.Equal(t, int64(2), listResp.Msg.Items[0].Version)
	assert.Equal(t, int64(1), listResp.Msg.Items[1].Version)
}

func TestListValuesRevisions_MissingDefinitionID(t *testing.T) {
	f := newValuesRevisionFixture(t)

	req := connect.NewRequest(&orchestratorv1.ListValuesRevisionsRequest{})
	_, err := f.svc.ListValuesRevisions(f.ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestDiscardValuesRevision_Success(t *testing.T) {
	f := newValuesRevisionFixture(t)

	createReq := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 3`,
	})
	createReq.Header().Set("Idempotency-Key", "discard-test-1")
	createResp, err := f.svc.CreateValuesRevision(f.ctx, createReq)
	require.NoError(t, err)

	discardReq := connect.NewRequest(&orchestratorv1.DiscardValuesRevisionRequest{
		RevisionId:           createResp.Msg.Revision.Id,
		ExpectedStateVersion: 1,
	})
	discardReq.Header().Set("Idempotency-Key", "discard-1")
	discardResp, err := f.svc.DiscardValuesRevision(f.ctx, discardReq)
	require.NoError(t, err)
	assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_DRAFT, discardResp.Msg.PreviousState)
	assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_DISCARDED, discardResp.Msg.NewState)
}

func TestDiscardValuesRevision_IdempotentAndConflict(t *testing.T) {
	f := newValuesRevisionFixture(t)
	createRequest := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 3`,
	})
	createRequest.Header().Set("Idempotency-Key", "discard-idempotent-create")
	created, err := f.svc.CreateValuesRevision(f.ctx, createRequest)
	require.NoError(t, err)

	discardRequest := connect.NewRequest(&orchestratorv1.DiscardValuesRevisionRequest{
		RevisionId:           created.Msg.Revision.Id,
		ExpectedStateVersion: created.Msg.Revision.StateVersion,
		Comment:              "obsolete draft",
	})
	discardRequest.Header().Set("Idempotency-Key", "discard-idempotent")
	first, err := f.svc.DiscardValuesRevision(f.ctx, discardRequest)
	require.NoError(t, err)
	second, err := f.svc.DiscardValuesRevision(f.ctx, discardRequest)
	require.NoError(t, err)
	assert.Equal(t, first.Msg.Revision.Id, second.Msg.Revision.Id)

	conflictRequest := connect.NewRequest(&orchestratorv1.DiscardValuesRevisionRequest{
		RevisionId:           created.Msg.Revision.Id,
		ExpectedStateVersion: created.Msg.Revision.StateVersion,
		Comment:              "different comment",
	})
	conflictRequest.Header().Set("Idempotency-Key", "discard-idempotent")
	_, err = f.svc.DiscardValuesRevision(f.ctx, conflictRequest)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

	decisions, err := f.st.ValuesApprovalEvidence().ListDecisions(f.ctx, created.Msg.Revision.Id)
	require.NoError(t, err)
	assert.Len(t, decisions, 1)
}

func TestDiscardValuesRevision_RejectsNonCreator(t *testing.T) {
	f := newValuesRevisionFixture(t)
	createRequest := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 3`,
	})
	createRequest.Header().Set("Idempotency-Key", "discard-noncreator-create")
	created, err := f.svc.CreateValuesRevision(f.ctx, createRequest)
	require.NoError(t, err)

	otherUserID := "other-vr"
	require.NoError(t, f.st.Users().Create(f.ctx, &store.User{ID: otherUserID, Username: otherUserID, PasswordHash: "unused"}))
	require.NoError(t, f.st.OrgMembers().Create(f.ctx, &store.OrganizationMember{OrgID: f.orgID, UserID: otherUserID, Role: store.RoleDeployer}))
	otherContext := authctx.WithActor(context.Background(), authctx.Actor{
		UserID:         otherUserID,
		OrganizationID: f.orgID,
		Roles:          []string{string(store.RoleDeployer)},
	})
	discardRequest := connect.NewRequest(&orchestratorv1.DiscardValuesRevisionRequest{
		RevisionId:           created.Msg.Revision.Id,
		ExpectedStateVersion: created.Msg.Revision.StateVersion,
	})
	discardRequest.Header().Set("Idempotency-Key", "discard-noncreator")
	_, err = f.svc.DiscardValuesRevision(otherContext, discardRequest)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	persisted, getErr := f.st.Values().Get(f.ctx, created.Msg.Revision.Id)
	require.NoError(t, getErr)
	assert.Equal(t, store.ValuesStatusDraft, persisted.Status)
}

func TestDiscardValuesRevision_RejectsRevokedMembership(t *testing.T) {
	f := newValuesRevisionFixture(t)
	createRequest := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 3`,
	})
	createRequest.Header().Set("Idempotency-Key", "discard-revoked-create")
	created, err := f.svc.CreateValuesRevision(f.ctx, createRequest)
	require.NoError(t, err)
	require.NoError(t, f.st.OrgMembers().Delete(f.ctx, f.orgID, f.creatorID))

	discardRequest := connect.NewRequest(&orchestratorv1.DiscardValuesRevisionRequest{
		RevisionId:           created.Msg.Revision.Id,
		ExpectedStateVersion: created.Msg.Revision.StateVersion,
	})
	discardRequest.Header().Set("Idempotency-Key", "discard-revoked")
	_, err = f.svc.DiscardValuesRevision(f.ctx, discardRequest)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	persisted, getErr := f.st.Values().Get(f.ctx, created.Msg.Revision.Id)
	require.NoError(t, getErr)
	assert.Equal(t, store.ValuesStatusDraft, persisted.Status)
}

func TestDiscardValuesRevision_InvalidStateVersion(t *testing.T) {
	f := newValuesRevisionFixture(t)

	req := connect.NewRequest(&orchestratorv1.DiscardValuesRevisionRequest{
		RevisionId:           "some-id",
		ExpectedStateVersion: 0,
	})
	req.Header().Set("Idempotency-Key", "discard-bad-1")
	_, err := f.svc.DiscardValuesRevision(f.ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func newPrepareFixture(t *testing.T) valuesRevisionFixture {
	f := newValuesRevisionFixture(t)

	require.NoError(t, f.st.Operations().Create(f.ctx, &store.Operation{
		ID:                  "op-vr-1",
		ReleaseDefinitionID: f.defID,
		OperationType:       store.OperationInstall,
		Status:              store.StatusPending,
		Actor:               store.ActorContext{UserID: f.creatorID},
	}))

	require.NoError(t, f.st.ConvergenceTasks().Create(f.ctx, &store.ConvergenceTask{
		ID:                  "task-vr-1",
		OperationID:         "op-vr-1",
		ReleaseDefinitionID: f.defID,
		Action:              store.EmergencySetContainerImage,
		TargetSummary:       "test",
		PromotionPaths:      json.RawMessage(`["spec.template.spec.containers[0].image"]`),
		Status:              "pending_promotion",
		SubmittedAt:         time.Now().UTC(),
		CreatedAt:           time.Now().UTC(),
	}))

	return f
}

func TestCreatePrepareSession_Success(t *testing.T) {
	f := newPrepareFixture(t)

	req := connect.NewRequest(&orchestratorv1.CreatePrepareSessionRequest{
		ReleaseDefinitionId: f.defID,
		TaskIds:             []string{"task-vr-1"},
	})
	resp, err := f.svc.CreatePrepareSession(f.ctx, req)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.Msg.PrepareToken)
	assert.NotZero(t, resp.Msg.ExpiresAt)
}

func TestCreatePrepareSession_NoAuth(t *testing.T) {
	f := newPrepareFixture(t)

	req := connect.NewRequest(&orchestratorv1.CreatePrepareSessionRequest{
		ReleaseDefinitionId: f.defID,
		TaskIds:             []string{"task-vr-1"},
	})
	_, err := f.svc.CreatePrepareSession(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestCreatePrepareSession_NoTasks(t *testing.T) {
	f := newPrepareFixture(t)

	req := connect.NewRequest(&orchestratorv1.CreatePrepareSessionRequest{
		ReleaseDefinitionId: f.defID,
		TaskIds:             []string{},
	})
	_, err := f.svc.CreatePrepareSession(f.ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestCreatePrepareSession_RejectsInvalidTaskSelections(t *testing.T) {
	f := newPrepareFixture(t)
	requests := []struct {
		name    string
		taskIDs []string
	}{
		{name: "duplicates", taskIDs: []string{"task-vr-1", "task-vr-1"}},
		{name: "unknown task", taskIDs: []string{"missing-task"}},
		{name: "too many", taskIDs: func() []string {
			ids := make([]string, 51)
			for index := range ids {
				ids[index] = fmt.Sprintf("task-%d", index)
			}
			return ids
		}()},
	}
	for _, testCase := range requests {
		t.Run(testCase.name, func(t *testing.T) {
			request := connect.NewRequest(&orchestratorv1.CreatePrepareSessionRequest{
				ReleaseDefinitionId: f.defID,
				TaskIds:             testCase.taskIDs,
			})
			_, err := f.svc.CreatePrepareSession(f.ctx, request)
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

func TestGetPrepareSession_Success(t *testing.T) {
	f := newPrepareFixture(t)

	createReq := connect.NewRequest(&orchestratorv1.CreatePrepareSessionRequest{
		ReleaseDefinitionId: f.defID,
		TaskIds:             []string{"task-vr-1"},
	})
	createResp, err := f.svc.CreatePrepareSession(f.ctx, createReq)
	require.NoError(t, err)

	getReq := connect.NewRequest(&orchestratorv1.GetPrepareSessionRequest{
		PrepareToken: createResp.Msg.PrepareToken,
	})
	getResp, err := f.svc.GetPrepareSession(f.ctx, getReq)
	require.NoError(t, err)
	assert.Equal(t, f.defID, getResp.Msg.ReleaseDefinitionId)
	assert.NotNil(t, getResp.Msg.ExpiresAt)
}

func TestGetPrepareSession_NotFound(t *testing.T) {
	f := newPrepareFixture(t)

	req := connect.NewRequest(&orchestratorv1.GetPrepareSessionRequest{
		PrepareToken: "nonexistent-token",
	})
	_, err := f.svc.GetPrepareSession(f.ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestGetPrepareSession_Expired(t *testing.T) {
	f := newPrepareFixture(t)
	token := "expired-prepare-token"
	session := &store.PrepareSession{
		TokenHash:           hashPrepareToken(token),
		ActorUserID:         f.creatorID,
		OrganizationID:      f.orgID,
		ReleaseDefinitionID: f.defID,
		TaskIDs:             []string{"task-vr-1"},
		LockedPaths:         []string{"spec.template.spec.containers[0].image"},
		LockedPathHash:      store.LockedPathHash([]string{"spec.template.spec.containers[0].image"}),
		ExpiresAt:           time.Now().UTC().Add(-time.Minute),
		CreatedAt:           time.Now().UTC().Add(-time.Hour),
	}
	require.NoError(t, f.st.PrepareSessions().Create(f.ctx, session, 0))
	request := connect.NewRequest(&orchestratorv1.GetPrepareSessionRequest{PrepareToken: token})
	_, err := f.svc.GetPrepareSession(f.ctx, request)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestCreateValuesRevision_ExpiredPrepareSessionLeavesTasksUnchanged(t *testing.T) {
	f := newPrepareFixture(t)
	token := "expired-create-token"
	lockedPaths := []string{"spec.template.spec.containers[0].image"}
	require.NoError(t, f.st.PrepareSessions().Create(f.ctx, &store.PrepareSession{
		TokenHash:           hashPrepareToken(token),
		ActorUserID:         f.creatorID,
		OrganizationID:      f.orgID,
		ReleaseDefinitionID: f.defID,
		TaskIDs:             []string{"task-vr-1"},
		LockedPaths:         lockedPaths,
		LockedPathHash:      store.LockedPathHash(lockedPaths),
		ExpiresAt:           time.Now().UTC().Add(-time.Minute),
		CreatedAt:           time.Now().UTC().Add(-time.Hour),
	}, 0))

	request := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `image: null`,
		PrepareToken:        token,
	})
	request.Header().Set("Idempotency-Key", "expired-create")
	_, err := f.svc.CreateValuesRevision(f.ctx, request)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Equal(t, "prepare_token_expired", approvalReasonCode(t, err))

	session, err := f.st.PrepareSessions().Get(f.ctx, hashPrepareToken(token))
	require.NoError(t, err)
	assert.Nil(t, session.ConsumedAt)
	task, err := f.st.ConvergenceTasks().Get(f.ctx, "task-vr-1")
	require.NoError(t, err)
	assert.Equal(t, "pending_promotion", task.Status)
	assert.Nil(t, task.ActiveRevisionID)
	page, err := f.st.Values().ListPage(f.ctx, store.ValuesListFilter{ReleaseDefinitionID: f.defID})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

func TestCreateValuesRevision_PrepareTokenConsumedOnce(t *testing.T) {
	f := newPrepareFixture(t)
	prepareRequest := connect.NewRequest(&orchestratorv1.CreatePrepareSessionRequest{
		ReleaseDefinitionId: f.defID,
		TaskIds:             []string{"task-vr-1"},
	})
	prepared, err := f.svc.CreatePrepareSession(f.ctx, prepareRequest)
	require.NoError(t, err)

	create := func(key string) error {
		request := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
			ReleaseDefinitionId: f.defID,
			Document:            `image: null`,
			PrepareToken:        prepared.Msg.PrepareToken,
		})
		request.Header().Set("Idempotency-Key", key)
		_, createErr := f.svc.CreateValuesRevision(f.ctx, request)
		return createErr
	}
	require.NoError(t, create("prepare-consume-first"))
	err = create("prepare-consume-second")
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
}

// AC-018-08: concurrent consumption of one prepare token — exactly one create wins and binds the tasks.
func TestCreateValuesRevision_PrepareTokenConcurrentConsumption(t *testing.T) {
	f := newPrepareFixture(t)
	prepareRequest := connect.NewRequest(&orchestratorv1.CreatePrepareSessionRequest{
		ReleaseDefinitionId: f.defID,
		TaskIds:             []string{"task-vr-1"},
	})
	prepared, err := f.svc.CreatePrepareSession(f.ctx, prepareRequest)
	require.NoError(t, err)

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for index := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			request := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
				ReleaseDefinitionId: f.defID,
				Document:            `image: null`,
				PrepareToken:        prepared.Msg.PrepareToken,
			})
			request.Header().Set("Idempotency-Key", fmt.Sprintf("prepare-concurrent-%d", index))
			_, createErr := f.svc.CreateValuesRevision(f.ctx, request)
			results <- createErr
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var successes, consumed int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case connect.CodeOf(err) == connect.CodeAlreadyExists && strings.Contains(err.Error(), "prepare_token_consumed"):
			consumed++
		default:
			require.NoErrorf(t, err, "unexpected concurrent create error: %v", err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, consumed)

	// Exactly one revision exists and the task is bound to it.
	page, err := f.st.Values().ListPage(f.ctx, store.ValuesListFilter{ReleaseDefinitionID: f.defID})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
	task, err := f.st.ConvergenceTasks().Get(f.ctx, "task-vr-1")
	require.NoError(t, err)
	require.NotNil(t, task.ActiveRevisionID)
	assert.Equal(t, page.Items[0].ID, *task.ActiveRevisionID)
}

// AC-018-10 + AC-018-22: a task already bound to another revision fails the converged create,
// rolls the whole transaction back, and does NOT consume the prepare session.
func TestCreateValuesRevision_ConvergenceTaskConflictRollsBackSession(t *testing.T) {
	f := newPrepareFixture(t)
	prepareRequest := connect.NewRequest(&orchestratorv1.CreatePrepareSessionRequest{
		ReleaseDefinitionId: f.defID,
		TaskIds:             []string{"task-vr-1"},
	})
	prepared, err := f.svc.CreatePrepareSession(f.ctx, prepareRequest)
	require.NoError(t, err)

	// Simulate drift: the task is already bound to another revision.
	require.NoError(t, f.st.ConvergenceTasks().BindRevision(f.ctx, "task-vr-1", "some-other-revision", "draft"))

	request := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `image: null`,
		PrepareToken:        prepared.Msg.PrepareToken,
	})
	request.Header().Set("Idempotency-Key", "drift-task-bound")
	_, err = f.svc.CreateValuesRevision(f.ctx, request)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "convergence_revision_exists")

	// The failed attempt must not consume the session (AC-018-10 rollback).
	session, err := f.st.PrepareSessions().Get(f.ctx, hashPrepareToken(prepared.Msg.PrepareToken))
	require.NoError(t, err)
	assert.Nil(t, session.ConsumedAt)
}

// AC-018-10: chain-head drift between Prepare and Create fails with parent_conflict
// and rolls back without consuming the session.
func TestCreateValuesRevision_PrepareChainHeadDriftRollsBack(t *testing.T) {
	f := newPrepareFixture(t)

	// Establish chain head version 1.
	initial := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 1`,
	})
	initial.Header().Set("Idempotency-Key", "drift-initial")
	created, err := f.svc.CreateValuesRevision(f.ctx, initial)
	require.NoError(t, err)

	prepareRequest := connect.NewRequest(&orchestratorv1.CreatePrepareSessionRequest{
		ReleaseDefinitionId:   f.defID,
		TaskIds:               []string{"task-vr-1"},
		ExpectedParentVersion: created.Msg.Revision.Version,
	})
	prepared, err := f.svc.CreatePrepareSession(f.ctx, prepareRequest)
	require.NoError(t, err)
	require.Equal(t, created.Msg.Revision.Id, prepared.Msg.ParentRevisionId)

	// Chain head drifts: version 2 commits before the prepared create.
	other := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId:   f.defID,
		Document:              `replicas: 2`,
		ParentRevisionId:      created.Msg.Revision.Id,
		ExpectedParentVersion: 1,
	})
	other.Header().Set("Idempotency-Key", "drift-other")
	_, err = f.svc.CreateValuesRevision(f.ctx, other)
	require.NoError(t, err)

	request := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `image: null`,
		PrepareToken:        prepared.Msg.PrepareToken,
	})
	request.Header().Set("Idempotency-Key", "drift-prepared")
	_, err = f.svc.CreateValuesRevision(f.ctx, request)
	require.Error(t, err)
	assert.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "parent_conflict")

	session, err := f.st.PrepareSessions().Get(f.ctx, hashPrepareToken(prepared.Msg.PrepareToken))
	require.NoError(t, err)
	assert.Nil(t, session.ConsumedAt)
}

func TestCreateValuesRevision_LockedPathDriftRollsBackSession(t *testing.T) {
	f := newPrepareFixture(t)
	prepareRequest := connect.NewRequest(&orchestratorv1.CreatePrepareSessionRequest{
		ReleaseDefinitionId: f.defID,
		TaskIds:             []string{"task-vr-1"},
	})
	prepared, err := f.svc.CreatePrepareSession(f.ctx, prepareRequest)
	require.NoError(t, err)

	_, err = f.st.DB().ExecContext(f.ctx, `
		UPDATE convergence_tasks SET promotion_paths = ? WHERE id = ?
	`, []byte(`["spec.template.spec.containers[0].tag"]`), "task-vr-1")
	require.NoError(t, err)

	request := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `image: null`,
		PrepareToken:        prepared.Msg.PrepareToken,
	})
	request.Header().Set("Idempotency-Key", "drift-locked-path")
	_, err = f.svc.CreateValuesRevision(f.ctx, request)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Equal(t, "convergence_task_conflict", approvalReasonCode(t, err))

	session, err := f.st.PrepareSessions().Get(f.ctx, hashPrepareToken(prepared.Msg.PrepareToken))
	require.NoError(t, err)
	assert.Nil(t, session.ConsumedAt)
	task, err := f.st.ConvergenceTasks().Get(f.ctx, "task-vr-1")
	require.NoError(t, err)
	assert.Nil(t, task.ActiveRevisionID)
	page, err := f.st.Values().ListPage(f.ctx, store.ValuesListFilter{ReleaseDefinitionID: f.defID})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

// AC-018-26: a discarded revision stays usable as content parent for a new draft.
func TestCreateValuesRevision_DiscardedParentReusable(t *testing.T) {
	f := newValuesRevisionFixture(t)
	createRequest := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 2`,
	})
	createRequest.Header().Set("Idempotency-Key", "discard-parent-create")
	created, err := f.svc.CreateValuesRevision(f.ctx, createRequest)
	require.NoError(t, err)
	discardRequest := connect.NewRequest(&orchestratorv1.DiscardValuesRevisionRequest{
		RevisionId:           created.Msg.Revision.Id,
		ExpectedStateVersion: created.Msg.Revision.StateVersion,
	})
	discardRequest.Header().Set("Idempotency-Key", "discard-parent-discard")
	discarded, err := f.svc.DiscardValuesRevision(f.ctx, discardRequest)
	require.NoError(t, err)
	assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_DISCARDED, discarded.Msg.NewState)

	childRequest := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId:   f.defID,
		Document:              `replicas: 3`,
		ParentRevisionId:      created.Msg.Revision.Id,
		ExpectedParentVersion: 1,
	})
	childRequest.Header().Set("Idempotency-Key", "discard-parent-child")
	child, err := f.svc.CreateValuesRevision(f.ctx, childRequest)
	require.NoError(t, err)
	assert.Equal(t, int64(2), child.Msg.Revision.Version)
	assert.Equal(t, created.Msg.Revision.Id, child.Msg.Revision.ParentRevisionId)
	assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_DRAFT, child.Msg.Revision.Status)

	submitRequest := connect.NewRequest(&orchestratorv1.SubmitValuesRevisionRequest{
		RevisionId: child.Msg.Revision.Id, ExpectedStateVersion: child.Msg.Revision.StateVersion,
	})
	submitRequest.Header().Set("Idempotency-Key", "discard-parent-child-submit")
	submitted, err := f.svc.SubmitValuesRevision(f.ctx, submitRequest)
	require.NoError(t, err)

	approveRequest := connect.NewRequest(&orchestratorv1.ApproveValuesRevisionRequest{
		RevisionId: child.Msg.Revision.Id, ExpectedStateVersion: submitted.Msg.Revision.StateVersion,
	})
	approveRequest.Header().Set("Idempotency-Key", "discard-parent-child-approve")
	approved, err := f.svc.ApproveValuesRevision(
		f.adminContext(), approveRequest,
	)
	require.NoError(t, err)
	assert.Equal(t, commonv1.ValuesStatus_VALUES_STATUS_APPROVED, approved.Msg.NewState)
}

// AC-018-23: revision content is immutable across reads and the discarded terminal transition.
func TestValuesRevisionContentImmutable(t *testing.T) {
	f := newValuesRevisionFixture(t)
	createRequest := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            "database:\n  password: null\nimage:\n  tag: v1\n",
		SecretRefs: []*commonv1.SecretRef{
			{Path: "/database/password", Name: "database-secret", Key: "password"},
		},
	})
	createRequest.Header().Set("Idempotency-Key", "immutable-create")
	created, err := f.svc.CreateValuesRevision(f.ctx, createRequest)
	require.NoError(t, err)

	getRequest := connect.NewRequest(&orchestratorv1.GetValuesRevisionRequest{RevisionId: created.Msg.Revision.Id})
	first, err := f.svc.GetValuesRevision(f.ctx, getRequest)
	require.NoError(t, err)

	discardRequest := connect.NewRequest(&orchestratorv1.DiscardValuesRevisionRequest{
		RevisionId:           created.Msg.Revision.Id,
		ExpectedStateVersion: created.Msg.Revision.StateVersion,
	})
	discardRequest.Header().Set("Idempotency-Key", "immutable-discard")
	_, err = f.svc.DiscardValuesRevision(f.ctx, discardRequest)
	require.NoError(t, err)

	second, err := f.svc.GetValuesRevision(f.ctx, getRequest)
	require.NoError(t, err)
	assert.Equal(t, first.Msg.CanonicalDocument, second.Msg.CanonicalDocument)
	assert.Equal(t, first.Msg.Digest, second.Msg.Digest)
	assert.Equal(t, first.Msg.SecretRefs, second.Msg.SecretRefs)
	assert.Equal(t, first.Msg.ParentRevisionId, second.Msg.ParentRevisionId)
	assert.Equal(t, first.Msg.CreatedByUserId, second.Msg.CreatedByUserId)
	assert.Equal(t, first.Msg.Version, second.Msg.Version)
}

// AC-018-07: discarded is terminal for Submit, Approve, and Reject.
func TestDiscardedRevisionRejectsApprovalActions(t *testing.T) {
	f := newValuesRevisionFixture(t)
	createRequest := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `replicas: 2`,
	})
	createRequest.Header().Set("Idempotency-Key", "discarded-actions-create")
	created, err := f.svc.CreateValuesRevision(f.ctx, createRequest)
	require.NoError(t, err)

	discardRequest := connect.NewRequest(&orchestratorv1.DiscardValuesRevisionRequest{
		RevisionId:           created.Msg.Revision.Id,
		ExpectedStateVersion: created.Msg.Revision.StateVersion,
	})
	discardRequest.Header().Set("Idempotency-Key", "discarded-actions-discard")
	_, err = f.svc.DiscardValuesRevision(f.ctx, discardRequest)
	require.NoError(t, err)

	persisted, err := f.st.Values().Get(f.ctx, created.Msg.Revision.Id)
	require.NoError(t, err)
	actions := []struct {
		name string
		call func() error
	}{
		{
			name: "submit",
			call: func() error {
				request := connect.NewRequest(&orchestratorv1.SubmitValuesRevisionRequest{
					RevisionId: created.Msg.Revision.Id, ExpectedStateVersion: persisted.StateVersion,
				})
				request.Header().Set("Idempotency-Key", "discarded-actions-submit")
				_, actionErr := f.svc.SubmitValuesRevision(f.ctx, request)
				return actionErr
			},
		},
		{
			name: "approve",
			call: func() error {
				request := connect.NewRequest(&orchestratorv1.ApproveValuesRevisionRequest{
					RevisionId: created.Msg.Revision.Id, ExpectedStateVersion: persisted.StateVersion,
				})
				request.Header().Set("Idempotency-Key", "discarded-actions-approve")
				_, actionErr := f.svc.ApproveValuesRevision(
					f.adminContext(), request,
				)
				return actionErr
			},
		},
		{
			name: "reject",
			call: func() error {
				request := connect.NewRequest(&orchestratorv1.RejectValuesRevisionRequest{
					RevisionId:           created.Msg.Revision.Id,
					ExpectedStateVersion: persisted.StateVersion,
					Reason:               "invalid draft",
				})
				request.Header().Set("Idempotency-Key", "discarded-actions-reject")
				_, actionErr := f.svc.RejectValuesRevision(
					f.adminContext(), request,
				)
				return actionErr
			},
		},
	}
	for _, action := range actions {
		t.Run(action.name, func(t *testing.T) {
			err := action.call()
			require.Error(t, err)
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			assert.Equal(t, "invalid_revision_state", approvalReasonCode(t, err))
			current, getErr := f.st.Values().Get(f.ctx, created.Msg.Revision.Id)
			require.NoError(t, getErr)
			assert.Equal(t, store.ValuesStatusDiscarded, current.Status)
		})
	}

	decisions, err := f.st.ValuesApprovalEvidence().ListDecisions(f.ctx, created.Msg.Revision.Id)
	require.NoError(t, err)
	assert.Len(t, decisions, 1)
}

// REQ-010 D-9: a replay of an already-succeeded converged create (same key and
// request hash) returns the first result with created=false instead of being
// blocked by the consumed prepare token.
func TestCreateValuesRevision_ConvergedIdempotentReplay(t *testing.T) {
	f := newPrepareFixture(t)
	prepareRequest := connect.NewRequest(&orchestratorv1.CreatePrepareSessionRequest{
		ReleaseDefinitionId: f.defID,
		TaskIds:             []string{"task-vr-1"},
	})
	prepared, err := f.svc.CreatePrepareSession(f.ctx, prepareRequest)
	require.NoError(t, err)

	request := connect.NewRequest(&orchestratorv1.CreateValuesRevisionRequest{
		ReleaseDefinitionId: f.defID,
		Document:            `image: null`,
		PrepareToken:        prepared.Msg.PrepareToken,
	})
	request.Header().Set("Idempotency-Key", "converged-replay")

	first, err := f.svc.CreateValuesRevision(f.ctx, request)
	require.NoError(t, err)
	assert.True(t, first.Msg.Created)

	replayed, err := f.svc.CreateValuesRevision(f.ctx, request)
	require.NoError(t, err)
	assert.False(t, replayed.Msg.Created)
	assert.Equal(t, first.Msg.Revision.Id, replayed.Msg.Revision.Id)

	// Exactly one revision and one binding exist.
	page, err := f.st.Values().ListPage(f.ctx, store.ValuesListFilter{ReleaseDefinitionID: f.defID})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1)
}

// REQ-018 安全边界: reads require a real-time membership query — a revoked
// org membership blocks GetPrepareSession immediately.
func TestGetPrepareSession_RevokedMembershipDenied(t *testing.T) {
	f := newPrepareFixture(t)
	prepareRequest := connect.NewRequest(&orchestratorv1.CreatePrepareSessionRequest{
		ReleaseDefinitionId: f.defID,
		TaskIds:             []string{"task-vr-1"},
	})
	prepared, err := f.svc.CreatePrepareSession(f.ctx, prepareRequest)
	require.NoError(t, err)

	require.NoError(t, f.st.OrgMembers().Delete(f.ctx, f.orgID, f.creatorID))

	request := connect.NewRequest(&orchestratorv1.GetPrepareSessionRequest{
		PrepareToken: prepared.Msg.PrepareToken,
	})
	_, err = f.svc.GetPrepareSession(f.ctx, request)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}
