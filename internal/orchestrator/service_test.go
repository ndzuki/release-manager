package orchestrator

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func setupService(t *testing.T) (*Service, store.Store, func()) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	st, err := sqlitestore.Open(dbPath)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc := NewService(st, logger)

	return svc, st, func() { st.Close() }
}

func seedDefinition(t *testing.T, st store.Store) {
	t.Helper()
	def := &store.ReleaseDefinition{
		ID:          "def-001",
		Name:        "my-release",
		CustomerID:  "cust-001",
		ClusterID:   "cls-001",
		Namespace:   "default",
		ReleaseName: "my-release",
		ChartName:   "nginx",
		Status:      store.DefStatusActive,
		CreatedBy:   "test",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	err := st.Definitions().Create(context.Background(), def)
	require.NoError(t, err)
}

func TestCreateOperation_Install_Success(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	req := &orchestratorv1.CreateOperationRequest{
		OperationType:         "INSTALL",
		BundleId:              "bundle-001",
		ReleaseDefinitionId:   "def-001",
		ValuesRevisionId:      "vr-001",
		IdempotencyKey:        "idem-001",
		ExpectedCurrentRevision: 0,
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}

	resp, err := svc.CreateOperation(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.OperationId)
	assert.Equal(t, "preflight", resp.State) // standard ops enter preflight
	assert.NotNil(t, resp.AcceptedAt)
}

func TestCreateOperation_Idempotency(t *testing.T) {
	// AC-003-03: same idempotency_key returns original operation
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	req := &orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
		IdempotencyKey:      "idem-dup",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}

	resp1, err := svc.CreateOperation(context.Background(), req)
	require.NoError(t, err)

	resp2, err := svc.CreateOperation(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, resp1.OperationId, resp2.OperationId, "idempotent requests must return same operation")
}

func TestCreateOperation_ReleaseBusy(t *testing.T) {
	// AC-003-04: same definition, non-terminal operation → release_busy
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)

	req := &orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		BundleId:            "bundle-001",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-001",
		IdempotencyKey:      "idem-002",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}

	_, err := svc.CreateOperation(context.Background(), req)
	require.NoError(t, err)

	// Second request with different idempotency key → release_busy
	req2 := &orchestratorv1.CreateOperationRequest{
		OperationType:       "UPGRADE",
		BundleId:            "bundle-002",
		ReleaseDefinitionId: "def-001",
		ValuesRevisionId:    "vr-002",
		IdempotencyKey:      "idem-003",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}

	_, err = svc.CreateOperation(context.Background(), req2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "release_busy")
}

func TestCreateOperation_DefinitionNotFound(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	req := &orchestratorv1.CreateOperationRequest{
		OperationType:       "INSTALL",
		ReleaseDefinitionId: "nonexistent",
		IdempotencyKey:      "idem-004",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}

	_, err := svc.CreateOperation(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestCreateOperation_InvalidType(t *testing.T) {
	svc, _, cleanup := setupService(t)
	defer cleanup()

	req := &orchestratorv1.CreateOperationRequest{
		OperationType:       "INVALID",
		ReleaseDefinitionId: "def-001",
		IdempotencyKey:      "idem-005",
		Actor: &commonv1.ActorContext{
			UserId:       "user-001",
			Organization: "org-001",
		},
	}

	_, err := svc.CreateOperation(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid operation_type")
}