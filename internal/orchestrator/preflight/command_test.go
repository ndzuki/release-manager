package preflight

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

func TestCommandPayload_RollbackFields(t *testing.T) {
	// Verify that commandPayload() populates all rollback-relevant fields
	// including target_revision, expected_current_revision, etc.
	op := &store.Operation{
		ID:                  "op-001",
		OperationType:       store.OperationRollback,
		ReleaseDefinitionID: "def-001",
		BundleID:            "bundle-001",
		ValuesRevisionID:    "vr-001",
		ExpectedRevision:    3,
		TargetRevision:      1,
		ValuesPatch:         []byte(`{"key":"value"}`),
	}

	// A minimal stub that satisfies store.DefinitionStore.
	stubDefs := &stubDefinitionStore{
		def: &store.ReleaseDefinition{
			ID:          "def-001",
			Namespace:   "apps",
			ReleaseName: "example",
		},
	}

	c := &Coordinator{
		defs:           stubDefs,
		timeoutSeconds: 300,
	}

	payload, err := c.commandPayload(context.Background(), op, StageArtifact, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, StageArtifact, payload.Stage)
	assert.Equal(t, "op-001", payload.OperationID)
	assert.Equal(t, "bundle-001", payload.BundleID)
	assert.Equal(t, "def-001", payload.DefinitionID)
	assert.Equal(t, "apps", payload.Namespace)
	assert.Equal(t, "example", payload.ReleaseName)
	assert.Equal(t, int64(300), payload.TimeoutSeconds)
	assert.Equal(t, "vr-001", payload.ValuesRevisionID)
	assert.Equal(t, int64(3), payload.ExpectedCurrentRevision)
	assert.Equal(t, int64(1), payload.TargetRevision)
	assert.False(t, payload.Atomic) // rollback is never atomic
	assert.Equal(t, json.RawMessage(`{"key":"value"}`), payload.ValuesPatch)
}

// TestCommandPayload_InstallCreatesNamespace locks the INSTALL contract: the
// E2E fixture namespaces do not pre-exist in the customer clusters, so the
// artifact/render/cluster commands must carry create_namespace=true (real
// smoke 2026-08-27: namespaces "e2e-release" not found → helm install
// failed). Non-install operations must NOT create the namespace.
func TestCommandPayload_InstallCreatesNamespace(t *testing.T) {
	stubDefs := &stubDefinitionStore{
		def: &store.ReleaseDefinition{ID: "def-001", Namespace: "e2e-release", ReleaseName: "fixture"},
	}
	c := &Coordinator{defs: stubDefs, timeoutSeconds: 300}

	for _, tt := range []struct {
		opType     store.OperationType
		wantCreate bool
	}{
		{store.OperationInstall, true},
		{store.OperationUpgrade, false},
		{store.OperationRollback, false},
	} {
		op := &store.Operation{
			ID: "op-001", OperationType: tt.opType,
			ReleaseDefinitionID: "def-001", BundleID: "bundle-001",
		}
		payload, err := c.commandPayload(context.Background(), op, StageArtifact, nil, nil)
		require.NoError(t, err)
		assert.Equalf(t, tt.wantCreate, payload.CreateNamespace,
			"operation type %s must set create_namespace=%v", tt.opType, tt.wantCreate)

		// Round-trip: the field survives Marshal/UnmarshalCommandPayload so
		// the operator decodes it via DecodeCommandPayload.
		data, err := payload.Marshal()
		require.NoError(t, err)
		decoded, err := UnmarshalCommandPayload(data)
		require.NoError(t, err)
		assert.Equalf(t, tt.wantCreate, decoded.CreateNamespace,
			"operation type %s create_namespace lost in round-trip", tt.opType)
	}
}

func TestCommandPayload_RollbackRoundTrip(t *testing.T) {
	payload := &CommandPayload{
		Stage:                   StageArtifact,
		OperationID:             "op-001",
		DefinitionID:            "def-001",
		Namespace:               "apps",
		ReleaseName:             "example",
		TimeoutSeconds:          300,
		ExpectedCurrentRevision: 3,
		TargetRevision:          1,
	}

	data, err := payload.Marshal()
	require.NoError(t, err)

	// Verify key fields in the JSON.
	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &raw))

	assert.Equal(t, "artifact", raw["stage"])
	assert.Equal(t, float64(3), raw["expected_current_revision"])
	assert.Equal(t, float64(1), raw["target_revision"])

	// Unmarshal back.
	decoded, err := UnmarshalCommandPayload(data)
	require.NoError(t, err)
	assert.Equal(t, int64(3), decoded.ExpectedCurrentRevision)
	assert.Equal(t, int64(1), decoded.TargetRevision)
}

// stubDefinitionStore is a minimal in-memory implementation of store.DefinitionStore.
type stubDefinitionStore struct {
	def *store.ReleaseDefinition
}

func (s *stubDefinitionStore) Create(_ context.Context, _ *store.ReleaseDefinition, _ *store.ReleaseDefinitionEvent) error {
	return nil
}
func (s *stubDefinitionStore) Get(_ context.Context, id string) (*store.ReleaseDefinition, error) {
	if s.def != nil && s.def.ID == id {
		return s.def, nil
	}
	return nil, store.ErrNotFound
}
func (s *stubDefinitionStore) Update(_ context.Context, _ *store.ReleaseDefinition, _ *store.ReleaseDefinitionEvent) (*store.ReleaseDefinition, error) {
	return nil, nil
}
func (s *stubDefinitionStore) List(_ context.Context, _, _ string, _ bool) ([]*store.ReleaseDefinition, error) {
	return nil, nil
}
func (s *stubDefinitionStore) SetCurrentBundle(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}
