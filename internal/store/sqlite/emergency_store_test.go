package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

func TestEmergencyStoresCreateReplayAndConflict(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-emergency")

	first := emergencyCreateCommand(t, "def-emergency", "idem-1", "hash-1", store.EmergencySetReplicas)
	result, err := st.EmergencyIntents().CreateIfAvailable(ctx, first)
	require.NoError(t, err)
	assert.False(t, result.Replayed)
	assert.Equal(t, first.Operation.ID, result.Operation.ID)

	replay, err := st.EmergencyIntents().CreateIfAvailable(ctx, first)
	require.NoError(t, err)
	assert.True(t, replay.Replayed)
	assert.Equal(t, first.Operation.ID, replay.Operation.ID)

	conflict := emergencyCreateCommand(t, "def-emergency", "idem-2", "hash-2", store.EmergencySetReplicas)
	_, err = st.EmergencyIntents().CreateIfAvailable(ctx, conflict)
	assert.ErrorIs(t, err, store.ErrEmergencyConflict)

	differentField := emergencyCreateCommand(t, "def-emergency", "idem-3", "hash-3", store.EmergencySetContainerImage)
	_, err = st.EmergencyIntents().CreateIfAvailable(ctx, differentField)
	require.NoError(t, err)
}

func TestConvergenceTaskStoreLifecycle(t *testing.T) {
	st := OpenTest(t)
	ctx := context.Background()
	seedEmergencyDefinition(t, st, "def-convergence")
	command := emergencyCreateCommand(t, "def-convergence", "idem-convergence", "hash-convergence", store.EmergencySetContainerImage)
	result, err := st.EmergencyIntents().CreateIfAvailable(ctx, command)
	require.NoError(t, err)
	require.NotNil(t, result.ConvergenceTask)

	hasPending, err := st.ConvergenceTasks().HasPendingPromotionForDefinition(ctx, "def-convergence")
	require.NoError(t, err)
	assert.True(t, hasPending)

	hasPath, err := st.ConvergenceTasks().HasPendingPromotionPath(ctx, "def-convergence", []string{"image.digest"})
	require.NoError(t, err)
	assert.True(t, hasPath)

	require.NoError(t, st.ConvergenceTasks().BindRevision(ctx, result.ConvergenceTask.ID, "revision-1", "pending_approval"))
	require.NoError(t, st.ConvergenceTasks().MarkConverged(ctx, result.ConvergenceTask.ID, "revision-1"))

	task, err := st.ConvergenceTasks().GetByOperationID(ctx, result.Operation.ID)
	require.NoError(t, err)
	assert.Equal(t, "converged", task.Status)
	assert.Equal(t, "revision-1", *task.ActiveRevisionID)
}

func seedEmergencyDefinition(t *testing.T, st *Store, id string) {
	t.Helper()
	require.NoError(t, st.Definitions().Create(t.Context(), &store.ReleaseDefinition{
		ID: id, Name: id, CustomerID: "customer", ClusterID: "cluster", Namespace: "default",
		ReleaseName: id, Status: store.DefStatusActive, MaxEmergencyReplicas: 100,
	}, nil))
}

func emergencyCreateCommand(t *testing.T, definitionID, idempotencyKey, requestHash string, action store.EmergencyAction) store.EmergencyCreateCommand {
	t.Helper()
	now := time.Now().UTC()
	opID := uuid.NewString()
	intent := &store.EmergencyIntent{
		ID: uuid.NewString(), ReleaseDefinitionID: definitionID, OperationID: opID, CommandID: uuid.NewString(),
		Action: action, WorkloadKind: "DEPLOYMENT", WorkloadName: "api", WorkloadNamespace: "default", WorkloadUID: "uid-api",
		Convergence: store.EmergencyRequirePromotion,
	}
	if action == store.EmergencySetReplicas {
		replicas := int32(3)
		intent.TargetReplicas = &replicas
	} else {
		container := "app"
		image := "registry.example/app@sha256:abc"
		intent.Container = &container
		intent.ImageReference = &image
	}
	paths, err := json.Marshal([]string{"image.digest"})
	require.NoError(t, err)
	intent.PromotionPaths = paths
	task := &store.ConvergenceTask{
		ID: uuid.NewString(), OperationID: opID, ReleaseDefinitionID: definitionID, Action: action,
		TargetSummary: "Deployment/api", Reason: "incident", PromotionPaths: paths,
	}
	hash := sha256.Sum256([]byte(idempotencyKey))
	return store.EmergencyCreateCommand{
		Operation: &store.Operation{
			ID: opID, OperationType: store.OperationEmergency, Status: store.StatusPending,
			ReleaseDefinitionID: definitionID, IdempotencyKey: hex.EncodeToString(hash[:]), RequestHash: requestHash,
			CreatedAt: now, UpdatedAt: now,
		},
		Intent: intent, ConvergenceTask: task,
		IdempotencyScope: "org:" + definitionID, IdempotencyKeyHash: hex.EncodeToString(hash[:]),
		RequestHash: requestHash, IdempotencyExpiresAt: now.Add(time.Hour),
	}
}
