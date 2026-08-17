package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func setupStore(t *testing.T) *sqlitestore.Store {
	t.Helper()
	return sqlitestore.OpenTest(t)
}

// setupFileStore opens a real on-disk SQLite database (WAL mode) so concurrent
// write transactions exercise the same locking semantics as production
// (modernc.org/sqlite's shared in-memory databases serialize oddly under
// concurrent BEGIN IMMEDIATE; the file store does not).
func setupFileStore(t *testing.T) *sqlitestore.Store {
	t.Helper()
	st, err := sqlitestore.Open(filepath.Join(t.TempDir(), "trust-concurrent.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	return st
}

func TestCustomerCreateAndGet(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	c := &store.Customer{
		ID:   uuid.New().String(),
		Name: "Acme Corp",
		Slug: "acme-corp",
	}

	err := st.Customers().Create(ctx, c)
	require.NoError(t, err)

	got, err := st.Customers().Get(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, c.ID, got.ID)
	assert.Equal(t, c.Name, got.Name)
	assert.Equal(t, c.Slug, got.Slug)
	assert.Equal(t, store.CustomerActive, got.Status)
}

func TestCustomerGetBySlug(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	c := &store.Customer{ID: uuid.New().String(), Name: "Beta Inc", Slug: "beta-inc"}
	require.NoError(t, st.Customers().Create(ctx, c))

	got, err := st.Customers().GetBySlug(ctx, "beta-inc")
	require.NoError(t, err)
	assert.Equal(t, c.ID, got.ID)
}

func TestCustomerNotFound(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	_, err := st.Customers().Get(ctx, "nonexistent")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestCustomerDisable(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	c := &store.Customer{ID: uuid.New().String(), Name: "Gamma", Slug: "gamma"}
	require.NoError(t, st.Customers().Create(ctx, c))

	c.Status = store.CustomerDisabled
	require.NoError(t, st.Customers().Update(ctx, c, c.Version))

	got, err := st.Customers().Get(ctx, c.ID)
	require.NoError(t, err)
	assert.Equal(t, store.CustomerDisabled, got.Status)
}

func TestCustomerList(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	c1 := &store.Customer{ID: uuid.New().String(), Name: "First", Slug: "first"}
	c2 := &store.Customer{ID: uuid.New().String(), Name: "Second", Slug: "second"}
	require.NoError(t, st.Customers().Create(ctx, c1))
	require.NoError(t, st.Customers().Create(ctx, c2))

	list, err := st.Customers().List(ctx, false)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list), 2)
}

func TestClusterCreateAndGet(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	// Need a customer first.
	cust := &store.Customer{ID: uuid.New().String(), Name: "Parent", Slug: "parent"}
	require.NoError(t, st.Customers().Create(ctx, cust))

	cl := &store.Cluster{
		ID:            uuid.New().String(),
		Name:          "prod-us-east",
		CustomerID:    cust.ID,
		KubeconfigRef: "ref-001",
	}
	require.NoError(t, st.Clusters().Create(ctx, cl))

	got, err := st.Clusters().Get(ctx, cl.ID)
	require.NoError(t, err)
	assert.Equal(t, cl.ID, got.ID)
	assert.Equal(t, cl.CustomerID, got.CustomerID)
	assert.Equal(t, store.ClusterActive, got.Status)
}

func TestClusterListByCustomer(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	cust := &store.Customer{ID: uuid.New().String(), Name: "Tenant", Slug: "tenant"}
	require.NoError(t, st.Customers().Create(ctx, cust))

	c1 := &store.Cluster{ID: uuid.New().String(), Name: "c1", CustomerID: cust.ID}
	c2 := &store.Cluster{ID: uuid.New().String(), Name: "c2", CustomerID: cust.ID}
	require.NoError(t, st.Clusters().Create(ctx, c1))
	require.NoError(t, st.Clusters().Create(ctx, c2))

	list, err := st.Clusters().List(ctx, cust.ID)
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestClusterDisable(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	cust := &store.Customer{ID: uuid.New().String(), Name: "X", Slug: "x"}
	require.NoError(t, st.Customers().Create(ctx, cust))

	cl := &store.Cluster{ID: uuid.New().String(), Name: "disabled-cluster", CustomerID: cust.ID}
	require.NoError(t, st.Clusters().Create(ctx, cl))

	cl.Status = store.ClusterDisabled
	require.NoError(t, st.Clusters().Update(ctx, cl, cl.Version))

	got, err := st.Clusters().Get(ctx, cl.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ClusterDisabled, got.Status)
}

func TestEnrollmentTokenLifecycle(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	cust := &store.Customer{ID: uuid.New().String(), Name: "EnrollCorp", Slug: "enroll"}
	require.NoError(t, st.Customers().Create(ctx, cust))

	cl := &store.Cluster{ID: uuid.New().String(), Name: "cluster1", CustomerID: cust.ID}
	require.NoError(t, st.Clusters().Create(ctx, cl))

	tok := &store.EnrollmentToken{
		ID:         uuid.New().String(),
		CustomerID: cust.ID,
		ClusterID:  cl.ID,
		Token:      "test-token-abc",
	}
	require.NoError(t, st.EnrollmentTokens().Create(ctx, tok))

	got, err := st.EnrollmentTokens().GetByToken(ctx, "test-token-abc")
	require.NoError(t, err)
	assert.Equal(t, store.TokenStatePending, got.State)

	// Mark used.
	require.NoError(t, st.EnrollmentTokens().MarkUsed(ctx, tok.ID, "op-001"))

	got, err = st.EnrollmentTokens().GetByToken(ctx, "test-token-abc")
	require.NoError(t, err)
	assert.Equal(t, store.TokenStateUsed, got.State)
	assert.Equal(t, "op-001", got.OperatorID)
}

func TestOperatorCreateAndGetByCertSerial(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	cust := &store.Customer{ID: uuid.New().String(), Name: "OpCorp", Slug: "opcorp"}
	require.NoError(t, st.Customers().Create(ctx, cust))

	cl := &store.Cluster{ID: uuid.New().String(), Name: "c", CustomerID: cust.ID}
	require.NoError(t, st.Clusters().Create(ctx, cl))

	op := &store.Operator{
		ID:         uuid.New().String(),
		CustomerID: cust.ID,
		ClusterID:  cl.ID,
		CertSerial: "ABC123",
	}
	require.NoError(t, st.Operators().Create(ctx, op))

	got, err := st.Operators().GetByCertSerial(ctx, "ABC123")
	require.NoError(t, err)
	assert.Equal(t, op.ID, got.ID)
	assert.Equal(t, store.OperatorActive, got.Status)
}

func TestOperatorCertSerialUnique(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	cust := &store.Customer{ID: uuid.New().String(), Name: "SerialCorp", Slug: "serialcorp"}
	require.NoError(t, st.Customers().Create(ctx, cust))

	firstCluster := &store.Cluster{ID: uuid.New().String(), Name: "c1", CustomerID: cust.ID}
	require.NoError(t, st.Clusters().Create(ctx, firstCluster))
	secondCluster := &store.Cluster{ID: uuid.New().String(), Name: "c2", CustomerID: cust.ID}
	require.NoError(t, st.Clusters().Create(ctx, secondCluster))

	first := &store.Operator{
		ID:         uuid.New().String(),
		CustomerID: cust.ID,
		ClusterID:  firstCluster.ID,
		CertSerial: "SERIAL-COLLISION",
	}
	require.NoError(t, st.Operators().Create(ctx, first))

	// ADR-018: a second operator with the same cert serial must fail — an
	// 80-bit DER-hash collision must never bind two operators to one
	// certificate (REQ-015 v1.1 operators_cert_serial_uq).
	collision := &store.Operator{
		ID:         uuid.New().String(),
		CustomerID: cust.ID,
		ClusterID:  secondCluster.ID,
		CertSerial: "SERIAL-COLLISION",
	}
	require.Error(t, st.Operators().Create(ctx, collision))
}

func TestSessionLifecycle(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	cust := &store.Customer{ID: uuid.New().String(), Name: "SessCorp", Slug: "sesscorp"}
	require.NoError(t, st.Customers().Create(ctx, cust))
	cl := &store.Cluster{ID: uuid.New().String(), Name: "c", CustomerID: cust.ID}
	require.NoError(t, st.Clusters().Create(ctx, cl))
	op := &store.Operator{ID: uuid.New().String(), CustomerID: cust.ID, ClusterID: cl.ID, CertSerial: "S1"}
	require.NoError(t, st.Operators().Create(ctx, op))

	sess := &store.Session{
		ID:         uuid.New().String(),
		OperatorID: op.ID,
		Status:     store.SessionOnline,
	}
	require.NoError(t, st.Sessions().Create(ctx, sess))

	// Heartbeat.
	require.NoError(t, st.Sessions().Heartbeat(ctx, sess.ID))

	// Transition to suspect → offline.
	require.NoError(t, st.Sessions().UpdateStatus(ctx, sess.ID, store.SessionSuspect))
	got, err := st.Sessions().Get(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, store.SessionSuspect, got.Status)

	require.NoError(t, st.Sessions().UpdateStatus(ctx, sess.ID, store.SessionOffline))
	got, err = st.Sessions().Get(ctx, sess.ID)
	require.NoError(t, err)
	assert.Equal(t, store.SessionOffline, got.Status)
}

func TestSessionEstablish(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	cust := &store.Customer{ID: uuid.New().String(), Name: "Session Reconnect", Slug: "session-reconnect"}
	require.NoError(t, st.Customers().Create(ctx, cust))
	cl := &store.Cluster{ID: uuid.New().String(), Name: "c", CustomerID: cust.ID}
	require.NoError(t, st.Clusters().Create(ctx, cl))
	op := &store.Operator{ID: uuid.New().String(), CustomerID: cust.ID, ClusterID: cl.ID, CertSerial: "SESSION-ESTABLISH"}
	require.NoError(t, st.Operators().Create(ctx, op))

	first := &store.Session{
		ID:                  uuid.New().String(),
		OperatorID:          op.ID,
		InstanceID:          "instance-1",
		Version:             "1.0.0",
		Capabilities:        map[string]string{"helm": "true"},
		ActiveConfigVersion: "config-v1",
		ExpiresAt:           time.Now().Add(time.Hour),
	}
	require.NoError(t, st.Sessions().Establish(ctx, first))

	got, err := st.Sessions().GetActiveByOperator(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, first.ID, got.ID)
	assert.Equal(t, first.InstanceID, got.InstanceID)
	assert.Equal(t, first.Capabilities, got.Capabilities)

	reconnect := &store.Session{
		ID:                  uuid.New().String(),
		OperatorID:          op.ID,
		InstanceID:          "instance-1",
		Version:             "1.0.1",
		Capabilities:        map[string]string{"helm": "true", "inventory": "true"},
		ActiveConfigVersion: "config-v2",
		ExpiresAt:           time.Now().Add(time.Hour),
	}
	require.NoError(t, st.Sessions().Establish(ctx, reconnect))

	old, err := st.Sessions().Get(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, store.SessionOffline, old.Status)
	got, err = st.Sessions().GetActiveByOperator(ctx, op.ID)
	require.NotNil(t, old.StatusReason)
	assert.Equal(t, store.SessionReasonSessionReplaced, *old.StatusReason)
	require.NotNil(t, old.ClosedAt)
	require.NoError(t, err)
	assert.Equal(t, reconnect.ID, got.ID)
	assert.Equal(t, "1.0.1", got.Version)
	assert.Equal(t, "config-v2", got.ActiveConfigVersion)

	duplicate := &store.Session{
		ID:         uuid.New().String(),
		OperatorID: op.ID,
		InstanceID: "instance-2",
		ExpiresAt:  time.Now().Add(time.Hour),
	}
	assert.ErrorIs(t, st.Sessions().Establish(ctx, duplicate), store.ErrDuplicateKey)

	got, err = st.Sessions().GetActiveByOperator(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, reconnect.ID, got.ID)
}

func TestOutboxStateMachine(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	cust := &store.Customer{ID: uuid.New().String(), Name: "OutboxCorp", Slug: "outbox"}
	require.NoError(t, st.Customers().Create(ctx, cust))
	cl := &store.Cluster{ID: uuid.New().String(), Name: "c", CustomerID: cust.ID}
	require.NoError(t, st.Clusters().Create(ctx, cl))
	op := &store.Operator{ID: uuid.New().String(), CustomerID: cust.ID, ClusterID: cl.ID, CertSerial: "O1"}
	require.NoError(t, st.Operators().Create(ctx, op))

	entry := &store.OutboxEntry{
		ID:          uuid.New().String(),
		OperationID: "op-1",
		OperatorID:  op.ID,
		Payload:     []byte(`{"chart":"nginx"}`),
		Status:      store.CommandPending,
		MaxInFlight: 1,
	}
	require.NoError(t, st.Outbox().Create(ctx, entry))

	// pending → delivered
	require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandDelivered, ""))
	got, err := st.Outbox().Get(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.CommandDelivered, got.Status)

	// delivered → persisted (ACK)
	require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandPersisted, ""))
	got, err = st.Outbox().Get(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.CommandPersisted, got.Status)

	// persisted → succeeded
	require.NoError(t, st.Outbox().UpdateStatus(ctx, entry.ID, store.CommandSucceeded, `{"release":"v1.2.3"}`))
	got, err = st.Outbox().Get(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.CommandSucceeded, got.Status)
	assert.Equal(t, `{"release":"v1.2.3"}`, got.ResultJSON)
}

func TestOperationTransition_OptimisticLockAndEvent(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	op := &store.Operation{
		ID:                  "operation-transition",
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      "operation-transition-key",
		RequestHash:         "request-hash",
		StateVersion:        4,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	updated, err := st.Operations().Transition(ctx, op.ID, store.StatusSucceeded, op.StateVersion, "")
	require.NoError(t, err)
	assert.Equal(t, store.StatusSucceeded, updated.Status)
	assert.Equal(t, 5, updated.StateVersion)

	var oldStatus, newStatus string
	var stateVersion int
	var operationType, definitionID string
	err = st.DB().QueryRowContext(ctx, `
		SELECT operation_type, release_definition_id, old_status, new_status, state_version
		FROM operation_events
		WHERE operation_id = ?
	`, op.ID).Scan(&operationType, &definitionID, &oldStatus, &newStatus, &stateVersion)
	require.NoError(t, err)
	assert.Equal(t, string(store.OperationInstall), operationType)
	assert.Equal(t, def.ID, definitionID)
	assert.Equal(t, string(store.StatusRunning), oldStatus)
	assert.Equal(t, string(store.StatusSucceeded), newStatus)
	assert.Equal(t, 5, stateVersion)

	rows, err := st.DB().QueryContext(ctx, `SELECT * FROM operation_events LIMIT 0`)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rows.Close()) })
	columns, err := rows.Columns()
	require.NoError(t, err)
	assert.NotContains(t, columns, "values_patch")
	assert.NotContains(t, columns, "actor")

	persisted, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusSucceeded, persisted.Status)
	assert.Equal(t, 5, persisted.StateVersion)
}

func TestOperationTransition_RejectsInvalidTarget(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	op := &store.Operation{
		ID:                  "operation-invalid-transition",
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      "operation-invalid-transition-key",
		RequestHash:         "request-hash",
		StateVersion:        4,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	_, err := st.Operations().Transition(ctx, op.ID, store.StatusPreflight, op.StateVersion, "")
	require.ErrorIs(t, err, store.ErrInvalidState)

	persisted, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusRunning, persisted.Status)
	assert.Equal(t, 4, persisted.StateVersion)
}

func TestOperationTransition_TerminalAt(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	// No preflight lifecycle row — terminal transition must still succeed
	// and terminal_at must be set (AC-023-08).
	op := &store.Operation{
		ID:                  "terminal-at-no-lifecycle",
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      "terminal-at-no-lifecycle-key",
		RequestHash:         "hash",
		StateVersion:        1,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	// Transition to succeeded (terminal).
	updated, err := st.Operations().Transition(ctx, op.ID, store.StatusSucceeded, op.StateVersion, "")
	require.NoError(t, err)
	assert.Equal(t, store.StatusSucceeded, updated.Status)
	require.NotNil(t, updated.TerminalAt, "returned Operation.TerminalAt must be non-nil")
	assert.False(t, updated.TerminalAt.IsZero())

	// Direct SQL: operations.terminal_at is set.
	var terminalAt *string
	err = st.DB().QueryRowContext(ctx, `SELECT terminal_at FROM operations WHERE id = ?`, op.ID).Scan(&terminalAt)
	require.NoError(t, err)
	require.NotNil(t, terminalAt, "operations.terminal_at must be non-nil after terminal transition")
	assert.NotEmpty(t, *terminalAt)

	// Verify terminal_at matches updated_at (migration history backfill concept).
	var updatedAtStr string
	err = st.DB().QueryRowContext(ctx, `SELECT updated_at FROM operations WHERE id = ?`, op.ID).Scan(&updatedAtStr)
	require.NoError(t, err)
	assert.Equal(t, *terminalAt, updatedAtStr, "terminal_at should equal updated_at (migration terminal_at backfill)")

	// Verify preflight_lifecycles was not affected (no row exists).
	var count int
	err = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM preflight_lifecycles WHERE operation_id = ?`, op.ID).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "no preflight lifecycle row should exist — transition handles missing lifecycle gracefully")
}

func TestOperationCreateIfAvailable_AllowsEmergencyPeers(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	first := &store.Operation{
		ID:                  "emergency-one",
		OperationType:       store.OperationEmergency,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      "emergency-one-key",
	}
	second := &store.Operation{
		ID:                  "emergency-two",
		OperationType:       store.OperationEmergency,
		Status:              store.StatusPending,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      "emergency-two-key",
	}
	require.NoError(t, st.Operations().CreateIfAvailable(ctx, first))
	require.NoError(t, st.Operations().CreateIfAvailable(ctx, second))

	standard := &store.Operation{
		ID:                  "standard-blocked",
		OperationType:       store.OperationUpgrade,
		Status:              store.StatusPending,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      "standard-blocked-key",
	}
	assert.ErrorIs(t, st.Operations().CreateIfAvailable(ctx, standard), store.ErrReleaseBusy)
}

func TestFinalizeUpgradeRollsBackOnInventoryFailure(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	def := createTestDefinition(t, st)
	op := &store.Operation{
		ID: "upgrade-transaction-rollback", OperationType: store.OperationUpgrade, Status: store.StatusRunning,
		ReleaseDefinitionID: def.ID, IdempotencyKey: "upgrade-transaction-rollback-key", RequestHash: "hash",
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	input := &store.UpgradeTerminalInput{
		OperationID: op.ID, ExpectedStateVersion: op.StateVersion, Status: store.StatusSucceeded,
		ResultPayload: []byte(`{"active":{"helm_revision":2}}`), ReleaseDefinitionID: def.ID,
		CustomerID: def.CustomerID, ClusterID: def.ClusterID, UpdateInventory: true,
		Revision: 2, ObservedManifestDigest: "sha256:manifest", LiveStatus: "deployed",
		InventoryStatus: store.InventoryActive, ResourceCount: 1,
	}

	err := st.UpgradeResults().FinalizeUpgrade(ctx, input)
	require.ErrorIs(t, err, store.ErrNotFound)
	persisted, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusRunning, persisted.Status)
	assert.Equal(t, op.StateVersion, persisted.StateVersion)
	_, err = st.ExecutionResults().Get(ctx, op.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.RolloutTrackings().Get(ctx, op.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
	var eventCount int
	require.NoError(t, st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM operation_events WHERE operation_id = ?`, op.ID).Scan(&eventCount))
	assert.Zero(t, eventCount)

	require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
		ReleaseDefinitionID: def.ID, CustomerID: def.CustomerID, ClusterID: def.ClusterID,
		Namespace: "apps", ReleaseName: "example", Revision: 1, Status: "deployed", InventoryStatus: store.InventoryActive,
	}))
	require.NoError(t, st.UpgradeResults().FinalizeUpgrade(ctx, input))
	persisted, err = st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusSucceeded, persisted.Status)
	result, err := st.ExecutionResults().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "upgrade", result.ResultType)
}

func TestGetNextPendingMaxInflight(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	cust := &store.Customer{ID: uuid.New().String(), Name: "FifoCorp", Slug: "fifo"}
	require.NoError(t, st.Customers().Create(ctx, cust))
	cl := &store.Cluster{ID: uuid.New().String(), Name: "c", CustomerID: cust.ID}
	require.NoError(t, st.Clusters().Create(ctx, cl))
	op := &store.Operator{ID: uuid.New().String(), CustomerID: cust.ID, ClusterID: cl.ID, CertSerial: "F1"}
	require.NoError(t, st.Operators().Create(ctx, op))

	e1 := &store.OutboxEntry{ID: "e1", OperationID: "op-1", OperatorID: op.ID, MaxInFlight: 1}
	e2 := &store.OutboxEntry{ID: "e2", OperationID: "op-2", OperatorID: op.ID, MaxInFlight: 1}
	require.NoError(t, st.Outbox().Create(ctx, e1))
	require.NoError(t, st.Outbox().Create(ctx, e2))

	// First pending is e1 (FIFO by created_at).
	got, err := st.Outbox().GetNextPending(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, "e1", got.ID)
}

// --- ValuesRevision tests (TASK-018) ---

func createTestDefinition(t *testing.T, st *sqlitestore.Store) *store.ReleaseDefinition {
	t.Helper()
	ctx := context.Background()
	def := &store.ReleaseDefinition{
		ID:         uuid.New().String(),
		Name:       "test-release",
		CustomerID: uuid.New().String(),
		ClusterID:  uuid.New().String(),
		Status:     store.DefStatusDraft,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))
	return def
}

func TestValuesRevisionCreateAndGet(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	vr := &store.ValuesRevision{
		ID:                  uuid.New().String(),
		ReleaseDefinitionID: def.ID,
		Version:             1,
		Status:              store.ValuesStatusDraft,
		CanonicalDocument:   []byte(`{"key":"value"}`),
		Digest:              "sha256:abc123",
		ParentRevisionID:    "",
	}

	err := st.Values().Create(ctx, vr)
	require.NoError(t, err)

	got, err := st.Values().Get(ctx, vr.ID)
	require.NoError(t, err)
	assert.Equal(t, vr.ID, got.ID)
	assert.Equal(t, vr.Digest, got.Digest)
	assert.Equal(t, store.ValuesStatusDraft, got.Status)
	assert.Equal(t, vr.Version, got.Version)
}

func TestValuesRevisionInitialParentPersistsAsNull(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	revision := &store.ValuesRevision{
		ID:                  uuid.NewString(),
		ReleaseDefinitionID: def.ID,
		Version:             1,
		Status:              store.ValuesStatusDraft,
		CanonicalDocument:   []byte(`{}`),
	}
	require.NoError(t, st.Values().Create(ctx, revision))

	var parentRevisionID *string
	require.NoError(t, st.DB().QueryRowContext(ctx,
		`SELECT parent_revision_id FROM values_revisions WHERE id = ?`, revision.ID,
	).Scan(&parentRevisionID))
	assert.Nil(t, parentRevisionID)
}

func TestValuesRevisionGetByDigest(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	vr := &store.ValuesRevision{
		ID:                  uuid.New().String(),
		ReleaseDefinitionID: def.ID,
		Version:             1,
		Status:              store.ValuesStatusDraft,
		CanonicalDocument:   []byte(`{}`),
		Digest:              "auth0:deadbeef",
	}
	require.NoError(t, st.Values().Create(ctx, vr))

	got, err := st.Values().GetByDigest(ctx, def.ID, "auth0:deadbeef")
	require.NoError(t, err)
	assert.Equal(t, vr.ID, got.ID)
}

func TestValuesLifecycleCreateDraftRequiresParentAfterInitial(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	initial := &store.ValuesRevision{
		ID:                  uuid.NewString(),
		ReleaseDefinitionID: def.ID,
		CanonicalDocument:   []byte(`{"replicas":1}`),
		Digest:              "sha256:initial",
	}
	first, err := st.ValuesLifecycle().CreateDraft(ctx, store.CreateValuesDraftCommand{
		Revision: initial,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), first.Revision.Version)

	_, err = st.ValuesLifecycle().CreateDraft(ctx, store.CreateValuesDraftCommand{
		Revision: &store.ValuesRevision{
			ID:                  uuid.NewString(),
			ReleaseDefinitionID: def.ID,
			CanonicalDocument:   []byte(`{"replicas":2}`),
			Digest:              "sha256:missing-parent",
		},
	})
	assert.ErrorIs(t, err, store.ErrDuplicateKey)
}

func TestValuesLifecycleCreateDraftConcurrentParentVersion(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	initial := &store.ValuesRevision{
		ID:                  uuid.NewString(),
		ReleaseDefinitionID: def.ID,
		CanonicalDocument:   []byte(`{"replicas":1}`),
		Digest:              "sha256:initial",
	}
	first, err := st.ValuesLifecycle().CreateDraft(ctx, store.CreateValuesDraftCommand{Revision: initial})
	require.NoError(t, err)

	results := make(chan error, 2)
	for index := range 2 {
		go func() {
			_, createErr := st.ValuesLifecycle().CreateDraft(ctx, store.CreateValuesDraftCommand{
				Revision: &store.ValuesRevision{
					ID:                  uuid.NewString(),
					ReleaseDefinitionID: def.ID,
					ParentRevisionID:    first.Revision.ID,
					CanonicalDocument:   []byte(fmt.Sprintf(`{"replicas":%d}`, index+2)),
					Digest:              fmt.Sprintf("sha256:concurrent-%d", index),
				},
				ExpectedParentVersion: first.Revision.Version,
			})
			results <- createErr
		}()
	}

	var successes, conflicts int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, store.ErrParentConflict):
			conflicts++
		default:
			require.NoErrorf(t, err, "unexpected concurrent create error: %T %v", err, err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, conflicts)
}

func TestValuesLifecycleDiscardIsIdempotentAndPreservesContent(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	created, err := st.ValuesLifecycle().CreateDraft(ctx, store.CreateValuesDraftCommand{
		Revision: &store.ValuesRevision{
			ID:                  uuid.NewString(),
			ReleaseDefinitionID: def.ID,
			CanonicalDocument:   []byte(`{"replicas":1}`),
			Digest:              "sha256:discard",
			SecretRefs:          []store.SecretRef{{Path: "/password", Name: "database", Key: "password"}},
		},
		ActorUserID: "creator",
	})
	require.NoError(t, err)
	command := store.DiscardValuesCommand{
		RevisionID:           created.Revision.ID,
		ExpectedStateVersion: created.Revision.StateVersion,
		ActorUserID:          "creator",
		IdempotencyScope:     "discard-values:creator:" + created.Revision.ID,
		IdempotencyKeyHash:   "discard-key",
		RequestHash:          "discard-request",
	}
	first, err := st.ValuesLifecycle().Discard(ctx, command)
	require.NoError(t, err)
	second, err := st.ValuesLifecycle().Discard(ctx, command)
	require.NoError(t, err)
	assert.False(t, first.Replayed)
	assert.True(t, second.Replayed)
	assert.Equal(t, created.Revision.CanonicalDocument, second.Revision.CanonicalDocument)
	assert.Equal(t, created.Revision.Digest, second.Revision.Digest)
	assert.Equal(t, created.Revision.SecretRefs, second.Revision.SecretRefs)

	decisions, err := st.ValuesApprovalEvidence().ListDecisions(ctx, created.Revision.ID)
	require.NoError(t, err)
	assert.Len(t, decisions, 1)
	auditEntries, err := st.ValuesApprovalEvidence().ListAuditOutbox(ctx, created.Revision.ID)
	require.NoError(t, err)
	assert.Len(t, auditEntries, 2)
}

func TestPrepareSessionDeleteExpiredRetainsRecentMetadata(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	sessions := []*store.PrepareSession{
		{TokenHash: "expired-old", ActorUserID: "user", OrganizationID: "org", ReleaseDefinitionID: createTestDefinition(t, st).ID, ParentVersion: 0, TaskIDs: []string{"task"}, LockedPaths: []string{"/replicas"}, LockedPathHash: "old", ExpiresAt: now.Add(-25 * time.Hour), CreatedAt: now.Add(-26 * time.Hour)},
		{TokenHash: "expired-recent", ActorUserID: "user", OrganizationID: "org", ReleaseDefinitionID: createTestDefinition(t, st).ID, ParentVersion: 0, TaskIDs: []string{"task"}, LockedPaths: []string{"/replicas"}, LockedPathHash: "recent", ExpiresAt: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour)},
	}
	for _, session := range sessions {
		require.NoError(t, st.PrepareSessions().Create(ctx, session, 0))
	}
	deleted, err := st.PrepareSessions().DeleteExpired(ctx, now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	_, err = st.PrepareSessions().Get(ctx, "expired-old")
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = st.PrepareSessions().Get(ctx, "expired-recent")
	require.NoError(t, err)
}

func TestValuesRevisionGetNextRevisionNumber(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	// First revision
	n, err := st.Values().GetNextRevisionNumber(ctx, def.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	vr := &store.ValuesRevision{
		ID:                  uuid.New().String(),
		ReleaseDefinitionID: def.ID,
		Version:             1,
		Status:              store.ValuesStatusDraft,
		CanonicalDocument:   []byte(`{}`),
		Digest:              "sha256:a",
	}
	require.NoError(t, st.Values().Create(ctx, vr))

	n, err = st.Values().GetNextRevisionNumber(ctx, def.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
}

func TestValuesRevisionList(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	parentRevisionID := ""
	for i := 1; i <= 3; i++ {
		vr := &store.ValuesRevision{
			ID:                  uuid.New().String(),
			ReleaseDefinitionID: def.ID,
			Version:             int64(i),
			Status:              store.ValuesStatusDraft,
			CanonicalDocument:   []byte(`{}`),
			Digest:              "sha256:" + string(rune('a'+i-1)),
			ParentRevisionID:    parentRevisionID,
		}
		require.NoError(t, st.Values().Create(ctx, vr))
		parentRevisionID = vr.ID
	}

	revs, err := st.Values().List(ctx, def.ID)
	require.NoError(t, err)
	assert.Len(t, revs, 3)
}

func TestValuesRevisionNotFound(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	_, err := st.Values().Get(ctx, "nonexistent")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestValuesRevisionApproveSupersedesPreviousApproved(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	previous := &store.ValuesRevision{
		ID:                  uuid.New().String(),
		ReleaseDefinitionID: def.ID,
		Version:             1,
		StateVersion:        1,
		Status:              store.ValuesStatusApproved,
		CanonicalDocument:   []byte(`{"key":"old"}`),
		Digest:              "sha256:old",
		CreatedByUserID:     "creator-old",
	}
	next := &store.ValuesRevision{
		ID:                  uuid.New().String(),
		ReleaseDefinitionID: def.ID,
		Version:             2,
		StateVersion:        1,
		Status:              store.ValuesStatusPendingApproval,
		CanonicalDocument:   []byte(`{"key":"new"}`),
		Digest:              "sha256:new",
		CreatedByUserID:     "creator-new",
		ParentRevisionID:    previous.ID,
	}
	require.NoError(t, st.Values().Create(ctx, previous))
	require.NoError(t, st.Values().Create(ctx, next))

	approvedResult, err := st.ValuesApproval().Approve(ctx, store.ValuesApprovalCommand{
		RevisionID: next.ID, ExpectedStateVersion: 1, ActorUserID: "approver-1", Authorized: true,
	})
	require.NoError(t, err)
	approved := approvedResult.Revision
	assert.Equal(t, next.ID, approved.ID)
	assert.Equal(t, store.ValuesStatusApproved, approved.Status)
	assert.EqualValues(t, 2, approved.StateVersion)
	assert.Equal(t, []string{previous.ID}, approvedResult.SupersededRevisionIDs)

	persistedPrevious, err := st.Values().Get(ctx, previous.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ValuesStatusSuperseded, persistedPrevious.Status)
}

func TestValuesApprovalRejectsUnauthorizedCommand(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	revision := &store.ValuesRevision{
		ID: uuid.New().String(), ReleaseDefinitionID: def.ID, Version: 1,
		StateVersion: 1, Status: store.ValuesStatusPendingApproval,
		CanonicalDocument: []byte(`{"key":"value"}`), Digest: "sha256:unauthorized",
		CreatedByUserID: "creator",
	}
	require.NoError(t, st.Values().Create(ctx, revision))

	_, err := st.ValuesApproval().Approve(ctx, store.ValuesApprovalCommand{
		RevisionID: revision.ID, ExpectedStateVersion: 1, ActorUserID: "approver",
	})
	require.ErrorIs(t, err, store.ErrNotAuthorized)
	persisted, err := st.Values().Get(ctx, revision.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ValuesStatusPendingApproval, persisted.Status)
	decisions, err := st.ValuesApprovalEvidence().ListDecisions(ctx, revision.ID)
	require.NoError(t, err)
	assert.Empty(t, decisions)
}

func TestValuesRevisionApproveRejectOptimisticLock(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	approval := &store.ValuesRevision{
		ID:                  uuid.New().String(),
		ReleaseDefinitionID: def.ID,
		Version:             1,
		StateVersion:        1,
		Status:              store.ValuesStatusPendingApproval,
		CanonicalDocument:   []byte(`{"key":"approve"}`),
		Digest:              "sha256:approve",
		CreatedByUserID:     "creator-approve",
	}
	rejection := &store.ValuesRevision{
		ID:                  uuid.New().String(),
		ReleaseDefinitionID: def.ID,
		Version:             2,
		StateVersion:        1,
		Status:              store.ValuesStatusDraft,
		CanonicalDocument:   []byte(`{"key":"reject"}`),
		Digest:              "sha256:reject",
		CreatedByUserID:     "creator-reject",
		ParentRevisionID:    approval.ID,
	}
	require.NoError(t, st.Values().Create(ctx, approval))
	require.NoError(t, st.Values().Create(ctx, rejection))

	approvedResult, err := st.ValuesApproval().Approve(ctx, store.ValuesApprovalCommand{
		RevisionID: approval.ID, ExpectedStateVersion: 1, ActorUserID: "approver-1", Authorized: true,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2, approvedResult.Revision.StateVersion)
	_, err = st.ValuesApproval().Approve(ctx, store.ValuesApprovalCommand{
		RevisionID: approval.ID, ExpectedStateVersion: 1, ActorUserID: "approver-2", Authorized: true,
	})
	assert.ErrorIs(t, err, store.ErrOptimisticLock)

	pendingResult, err := st.ValuesApproval().Submit(ctx, store.ValuesApprovalCommand{
		RevisionID: rejection.ID, ExpectedStateVersion: 1, ActorUserID: "creator-reject", Authorized: true,
	})
	require.NoError(t, err)
	rejectedResult, err := st.ValuesApproval().Reject(ctx, store.ValuesApprovalCommand{
		RevisionID: rejection.ID, ExpectedStateVersion: pendingResult.Revision.StateVersion,
		ActorUserID: "rejector-1", Reason: "needs changes", Authorized: true,
	})
	require.NoError(t, err)
	assert.Equal(t, store.ValuesStatusRejected, rejectedResult.Revision.Status)
	assert.EqualValues(t, 3, rejectedResult.Revision.StateVersion)
	_, err = st.ValuesApproval().Reject(ctx, store.ValuesApprovalCommand{
		RevisionID: rejection.ID, ExpectedStateVersion: pendingResult.Revision.StateVersion,
		ActorUserID: "rejector-2", Reason: "stale", Authorized: true,
	})
	assert.ErrorIs(t, err, store.ErrOptimisticLock)
}

// ── ReleaseDefinition store tests ───────────────────────────────

func TestDefinitionCreateDuplicateKey(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	def1 := &store.ReleaseDefinition{
		ID:                uuid.New().String(),
		Name:              "dup-def",
		CustomerID:        "cust-dup",
		ClusterID:         "cls-dup",
		Namespace:         "default",
		ReleaseName:       "same-name",
		ChartName:         "nginx",
		Status:            store.DefStatusActive,
		OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def1, nil))

	def2 := &store.ReleaseDefinition{
		ID:                uuid.New().String(),
		Name:              "dup-def-2",
		CustomerID:        "cust-dup",
		ClusterID:         "cls-dup",
		Namespace:         "default",
		ReleaseName:       "same-name",
		ChartName:         "nginx",
		Status:            store.DefStatusActive,
		OptimisticVersion: 1,
	}
	err := st.Definitions().Create(ctx, def2, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrDuplicateKey)
}

func TestDefinitionUpdateOptimisticLock(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	def := &store.ReleaseDefinition{
		ID:                uuid.New().String(),
		Name:              "lock-def",
		CustomerID:        uuid.New().String(),
		ClusterID:         uuid.New().String(),
		Namespace:         "ns",
		ReleaseName:       "lock-rel",
		Status:            store.DefStatusActive,
		OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def, nil))

	// Update with correct version succeeds.
	def.ReleaseName = "lock-rel-v2"
	updated, err := st.Definitions().Update(ctx, def, nil)
	require.NoError(t, err)
	assert.Equal(t, 2, updated.OptimisticVersion)

	// Update with old version fails.
	def.OptimisticVersion = 1 // stale
	_, err = st.Definitions().Update(ctx, def, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrOptimisticLock)
}

func TestDefinitionUpdateDuplicateKey(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	def1 := &store.ReleaseDefinition{
		ID:                uuid.New().String(),
		Name:              "key-def-1",
		CustomerID:        "cust-key",
		ClusterID:         "cls-key",
		Namespace:         "ns",
		ReleaseName:       "rel-a",
		Status:            store.DefStatusActive,
		OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def1, nil))

	def2 := &store.ReleaseDefinition{
		ID:                uuid.New().String(),
		Name:              "key-def-2",
		CustomerID:        "cust-key",
		ClusterID:         "cls-key",
		Namespace:         "ns",
		ReleaseName:       "rel-b",
		Status:            store.DefStatusActive,
		OptimisticVersion: 1,
	}
	require.NoError(t, st.Definitions().Create(ctx, def2, nil))

	// Try to update def2 to collide with def1's unique key.
	def2.ReleaseName = "rel-a"
	_, err := st.Definitions().Update(ctx, def2, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrDuplicateKey)
}

func TestDefinitionListFiltering(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	seed := func(custID, clsID, ns, rel string, status store.DefinitionStatus) {
		require.NoError(t, st.Definitions().Create(ctx, &store.ReleaseDefinition{
			ID:                uuid.New().String(),
			Name:              rel,
			CustomerID:        custID,
			ClusterID:         clsID,
			Namespace:         ns,
			ReleaseName:       rel,
			Status:            status,
			OptimisticVersion: 1,
		}, nil))
	}

	seed("cust-A", "cls-A", "ns1", "rel-1", store.DefStatusActive)
	seed("cust-A", "cls-A", "ns2", "rel-2", store.DefStatusActive)
	seed("cust-B", "cls-B", "ns1", "rel-3", store.DefStatusDisabled)

	// List by customer.
	defs, err := st.Definitions().List(ctx, "cust-A", "", false)
	require.NoError(t, err)
	assert.Len(t, defs, 2)

	// List all including disabled.
	defs, err = st.Definitions().List(ctx, "", "", true)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(defs), 3)

	// Normal list excludes disabled.
	defs, err = st.Definitions().List(ctx, "", "", false)
	require.NoError(t, err)
	for _, d := range defs {
		assert.NotEqual(t, store.DefStatusDisabled, d.Status)
	}
}

func TestDefinitionEventPersistence(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	def := &store.ReleaseDefinition{
		ID:                uuid.New().String(),
		Name:              "evt-def",
		CustomerID:        uuid.New().String(),
		ClusterID:         uuid.New().String(),
		ReleaseName:       "evt-rel",
		Status:            store.DefStatusActive,
		OptimisticVersion: 1,
	}
	event := &store.ReleaseDefinitionEvent{
		ID:           uuid.New().String(),
		DefinitionID: def.ID,
		EventType:    "definition_created",
	}
	require.NoError(t, st.Definitions().Create(ctx, def, event))

	events, err := st.DefinitionEvents().List(ctx, def.ID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "definition_created", events[0].EventType)
	assert.Equal(t, def.ID, events[0].DefinitionID)
}

func TestInventoryDefinitionAssociation(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()

	item := &store.ReleaseInventory{
		ReleaseDefinitionID: "definition-1",
		CustomerID:          "customer-1",
		ClusterID:           "cluster-1",
		Namespace:           "apps",
		ReleaseName:         "example",
		Chart:               "example-chart",
		ChartVersion:        "1.0.0",
		Revision:            1,
		Status:              "deployed",
		InventoryStatus:     store.InventoryActive,
		LastSyncID:          "sync-1",
	}
	require.NoError(t, st.Inventories().Upsert(ctx, item))

	items, err := st.Inventories().ListByCluster(ctx, "customer-1", "cluster-1")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "definition-1", items[0].ReleaseDefinitionID)
}

func TestOperatorManagement_CreateEnrollmentTokenAtomic(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	customerID, clusterID := seedOperatorManagementScope(t, st)

	first := &store.EnrollmentToken{
		ID:                   "token-first",
		CustomerID:           customerID,
		ClusterID:            clusterID,
		OperatorName:         "operator-a",
		Token:                "plaintext-first",
		CreatedByDisplayName: "admin",
		ExpiresAt:            time.Now().UTC().Add(time.Hour),
	}
	_, err := st.OperatorManagement().CreateEnrollmentToken(ctx, first, false, operatorAuditEvent("audit-create-first", clusterID, "operator.enrollment_token.created"))
	require.NoError(t, err)
	assert.NotEmpty(t, first.TokenHash)

	var persistedToken string
	require.NoError(t, st.DB().QueryRowContext(ctx, `SELECT token FROM enrollment_tokens WHERE id = ?`, first.ID).Scan(&persistedToken))
	assert.NotEqual(t, first.Token, persistedToken)
	assert.Equal(t, first.TokenHash, persistedToken)

	second := &store.EnrollmentToken{
		ID:                   "token-second",
		CustomerID:           customerID,
		ClusterID:            clusterID,
		OperatorName:         "operator-a",
		Token:                "plaintext-second",
		CreatedByDisplayName: "admin",
		ExpiresAt:            time.Now().UTC().Add(time.Hour),
	}
	_, err = st.OperatorManagement().CreateEnrollmentToken(ctx, second, false, operatorAuditEvent("audit-create-conflict", clusterID, "operator.enrollment_token.created"))
	assert.ErrorIs(t, err, store.ErrPendingTokenExists)

	failedReplacement := &store.EnrollmentToken{
		ID:                   "token-failed-replacement",
		CustomerID:           customerID,
		ClusterID:            clusterID,
		OperatorName:         "operator-a",
		Token:                "plaintext-failed-replacement",
		CreatedByDisplayName: "admin",
		ExpiresAt:            time.Now().UTC().Add(time.Hour),
	}
	_, err = st.OperatorManagement().CreateEnrollmentToken(ctx, failedReplacement, true, &store.AuditEvent{ID: "invalid-audit"})
	assert.ErrorIs(t, err, store.ErrAuditUnavailable)
	unchanged, err := st.EnrollmentTokens().ListByCluster(ctx, clusterID)
	require.NoError(t, err)
	require.Len(t, unchanged, 1)
	assert.Equal(t, first.ID, unchanged[0].ID)
	assert.Equal(t, store.TokenStatePending, unchanged[0].State)

	replaced, err := st.OperatorManagement().CreateEnrollmentToken(ctx, second, true, operatorAuditEvent("audit-replace", clusterID, "operator.enrollment_token.replaced"))
	require.NoError(t, err)
	assert.Equal(t, first.ID, replaced.PreviousID)

	old, err := st.EnrollmentTokens().ListByCluster(ctx, clusterID)
	require.NoError(t, err)
	require.Len(t, old, 2)
	assert.Equal(t, store.TokenStateRevoked, old[0].State)
	assert.Equal(t, second.ID, old[0].ReplacedByID)
}

func TestOperatorManagement_CreateEnrollmentTokenConcurrent(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	customerID, clusterID := seedOperatorManagementScope(t, st)
	start := make(chan struct{})
	results := make(chan error, 2)

	for index := range 2 {
		go func(index int) {
			<-start
			token := &store.EnrollmentToken{
				ID:                   fmt.Sprintf("token-concurrent-%d", index),
				CustomerID:           customerID,
				ClusterID:            clusterID,
				OperatorName:         fmt.Sprintf("operator-%d", index),
				Token:                fmt.Sprintf("plaintext-%d", index),
				CreatedByDisplayName: "admin",
				ExpiresAt:            time.Now().UTC().Add(time.Hour),
			}
			_, err := st.OperatorManagement().CreateEnrollmentToken(ctx, token, false, operatorAuditEvent(fmt.Sprintf("audit-concurrent-%d", index), clusterID, "operator.enrollment_token.created"))
			results <- err
		}(index)
	}
	close(start)

	var succeeded, conflicted int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, store.ErrPendingTokenExists):
			conflicted++
		default:
			require.NoErrorf(t, err, "unexpected concurrent create error: %T %v", err, err)
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, conflicted)

	tokens, err := st.EnrollmentTokens().ListByCluster(ctx, clusterID)
	require.NoError(t, err)
	var pending int
	for _, token := range tokens {
		if token.State == store.TokenStatePending {
			pending++
		}
	}
	assert.Equal(t, 1, pending)
}

func TestOperatorManagement_EnrollOperatorAtomic(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	customerID, clusterID := seedOperatorManagementScope(t, st)
	old := &store.Operator{ID: "operator-old", Name: "operator-old", CustomerID: customerID, ClusterID: clusterID, CertSerial: "serial-old"}
	require.NoError(t, st.Operators().Create(ctx, old))
	require.NoError(t, st.Sessions().Create(ctx, &store.Session{ID: "session-old", OperatorID: old.ID, CustomerID: customerID, ClusterID: clusterID, Status: store.SessionOnline}))
	token := &store.EnrollmentToken{
		ID: "token-enroll", CustomerID: customerID, ClusterID: clusterID, OperatorName: "operator-new",
		Token: "plaintext-enroll", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	require.NoError(t, st.EnrollmentTokens().Create(ctx, token))
	next := &store.Operator{ID: "operator-new", Name: "operator-new", CustomerID: customerID, ClusterID: clusterID, CertSerial: "serial-new"}
	session := &store.Session{ID: "session-new", Status: store.SessionOnline, Capabilities: map[string]string{"helm": "true"}, ExpiresAt: time.Now().UTC().Add(time.Hour)}

	result, err := st.OperatorManagement().EnrollOperator(ctx, token.ID, next, session)
	require.NoError(t, err)
	assert.Equal(t, old.ID, result.SupersededOperatorID)
	assert.Equal(t, customerID, result.Session.CustomerID)
	assert.Equal(t, clusterID, result.Session.ClusterID)

	used, err := st.EnrollmentTokens().GetByToken(ctx, token.Token)
	require.NoError(t, err)
	assert.Equal(t, store.TokenStateUsed, used.State)
	oldAfter, err := st.Operators().Get(ctx, old.ID)
	require.NoError(t, err)
	assert.Equal(t, store.OperatorSuperseded, oldAfter.Status)
	oldSession, err := st.Sessions().Get(ctx, "session-old")
	require.NoError(t, err)
	assert.Equal(t, store.SessionRevoked, oldSession.Status)
	require.NotNil(t, oldSession.StatusReason)
	assert.Equal(t, store.SessionReasonOperatorSuperseded, *oldSession.StatusReason)
}

func TestOperatorStore_GetActiveByNameUsesCustomerScope(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	firstCustomer, firstCluster := seedOperatorManagementScope(t, st)
	secondCustomer, secondCluster := seedOperatorManagementScope(t, st)
	require.NoError(t, st.Operators().Create(ctx, &store.Operator{ID: "operator-first", Name: "shared-name", CustomerID: firstCustomer, ClusterID: firstCluster, CertSerial: "serial-first"}))
	require.NoError(t, st.Operators().Create(ctx, &store.Operator{ID: "operator-second", Name: "shared-name", CustomerID: secondCustomer, ClusterID: secondCluster, CertSerial: "serial-second"}))

	got, err := st.Operators().GetActiveByName(ctx, secondCustomer, "shared-name")
	require.NoError(t, err)
	assert.Equal(t, "operator-second", got.ID)
}
func TestOperatorStore_ListByClusterFilterNoSession(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	customerID, clusterID := seedOperatorManagementScope(t, st)
	require.NoError(t, st.Operators().Create(ctx, &store.Operator{ID: "operator-without-session", Name: "without-session", CustomerID: customerID, ClusterID: clusterID, CertSerial: "serial-no-session"}))
	otherCustomerID, otherClusterID := seedOperatorManagementScope(t, st)
	withSession := &store.Operator{ID: "operator-with-session", Name: "with-session", CustomerID: otherCustomerID, ClusterID: otherClusterID, CertSerial: "serial-session"}
	require.NoError(t, st.Operators().Create(ctx, withSession))
	require.NoError(t, st.Sessions().Create(ctx, &store.Session{ID: "session-filter", OperatorID: withSession.ID, CustomerID: otherCustomerID, ClusterID: otherClusterID, Status: store.SessionOffline}))

	page, err := st.Operators().ListByClusterFilter(ctx, customerID, clusterID, store.OperatorListFilter{NoSession: true}, 20, nil)
	require.NoError(t, err)
	require.Len(t, page.Operators, 1)
	assert.Equal(t, "operator-without-session", page.Operators[0].ID)
}

func TestOperatorStore_ListByClusterFilterUsesLatestSession(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	customerID, clusterID := seedOperatorManagementScope(t, st)
	op := &store.Operator{ID: "operator-latest-session", Name: "latest-session", CustomerID: customerID, ClusterID: clusterID, CertSerial: "serial-latest-session"}
	require.NoError(t, st.Operators().Create(ctx, op))
	startedAt := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, st.Sessions().Create(ctx, &store.Session{
		ID: "session-old-offline", OperatorID: op.ID, CustomerID: customerID, ClusterID: clusterID,
		Status: store.SessionOffline, StartedAt: startedAt, LastHeartbeat: startedAt,
	}))
	require.NoError(t, st.Sessions().Create(ctx, &store.Session{
		ID: "session-new-online", OperatorID: op.ID, CustomerID: customerID, ClusterID: clusterID,
		Status: store.SessionOnline, StartedAt: startedAt.Add(time.Second), LastHeartbeat: startedAt.Add(time.Second),
	}))

	offline := store.SessionOffline
	offlinePage, err := st.Operators().ListByClusterFilter(ctx, customerID, clusterID, store.OperatorListFilter{SessionStatus: &offline}, 20, nil)
	require.NoError(t, err)
	assert.Empty(t, offlinePage.Operators)

	online := store.SessionOnline
	onlinePage, err := st.Operators().ListByClusterFilter(ctx, customerID, clusterID, store.OperatorListFilter{SessionStatus: &online}, 20, nil)
	require.NoError(t, err)
	require.Len(t, onlinePage.Operators, 1)
	assert.Equal(t, op.ID, onlinePage.Operators[0].ID)
}

func TestOperatorStore_ListByClusterFilterPaginatesSameTimestamp(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	customerID, clusterID := seedOperatorManagementScope(t, st)
	registeredAt := time.Date(2026, time.July, 27, 6, 0, 0, 123456789, time.UTC)
	for _, id := range []string{"operator-c", "operator-b", "operator-a"} {
		require.NoError(t, st.Operators().Create(ctx, &store.Operator{
			ID: id, Name: id, CustomerID: customerID, ClusterID: clusterID,
			CertSerial: "serial-" + id, Status: store.OperatorSuperseded,
			RegisteredAt: registeredAt, UpdatedAt: registeredAt,
		}))
	}

	first, err := st.Operators().ListByClusterFilter(ctx, customerID, clusterID, store.OperatorListFilter{}, 1, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"operator-c"}, []string{first.Operators[0].ID})
	require.NotNil(t, first.NextPageCursor)

	second, err := st.Operators().ListByClusterFilter(ctx, customerID, clusterID, store.OperatorListFilter{}, 1, first.NextPageCursor)
	require.NoError(t, err)
	require.Equal(t, []string{"operator-b"}, []string{second.Operators[0].ID})
	require.NotNil(t, second.NextPageCursor)

	third, err := st.Operators().ListByClusterFilter(ctx, customerID, clusterID, store.OperatorListFilter{}, 1, second.NextPageCursor)
	require.NoError(t, err)
	require.Equal(t, []string{"operator-a"}, []string{third.Operators[0].ID})
	assert.Nil(t, third.NextPageCursor)
}

func TestOperatorManagement_RevokeOperatorAtomicAndIdempotent(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	customerID, clusterID := seedOperatorManagementScope(t, st)
	op := &store.Operator{ID: "operator-revoke", Name: "operator-revoke", CustomerID: customerID, ClusterID: clusterID, CertSerial: "serial-revoke"}
	require.NoError(t, st.Operators().Create(ctx, op))
	session := &store.Session{ID: "session-revoke", OperatorID: op.ID, CustomerID: customerID, ClusterID: clusterID, Status: store.SessionOnline}
	require.NoError(t, st.Sessions().Create(ctx, session))

	result, err := st.OperatorManagement().RevokeOperator(ctx, customerID, clusterID, op.ID, "security incident", operatorAuditEvent("audit-revoke", op.ID, "operator.revoked"))
	require.NoError(t, err)
	assert.True(t, result.Changed)
	assert.Equal(t, store.OperatorRevoked, result.Operator.Status)
	assert.Equal(t, "security incident", result.Operator.RevokeReason)
	require.NotNil(t, result.Session)
	assert.Equal(t, store.SessionRevoked, result.Session.Status)

	secondAudit := operatorAuditEvent("audit-revoke-repeat", op.ID, "operator.revoked")
	again, err := st.OperatorManagement().RevokeOperator(ctx, customerID, clusterID, op.ID, "must not overwrite", secondAudit)
	require.NoError(t, err)
	assert.False(t, again.Changed)
	assert.Equal(t, "security incident", again.Operator.RevokeReason)
	assert.Equal(t, result.Operator.RevokedAt, again.Operator.RevokedAt)
	events, err := st.AuditEvents().ListByResource(ctx, "operator", op.ID)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, secondAudit.ID, events[1].ID)
}

func seedOperatorManagementScope(t *testing.T, st *sqlitestore.Store) (customerID, clusterID string) {
	t.Helper()
	ctx := context.Background()
	customerID = uuid.NewString()
	clusterID = uuid.NewString()
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: customerID, Name: "Operator customer", Slug: customerID}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: clusterID, Name: "Operator cluster", CustomerID: customerID}))
	return customerID, clusterID
}

func operatorAuditEvent(id, resourceID, action string) *store.AuditEvent {
	return &store.AuditEvent{
		ID:             id,
		ActorKind:      store.AuditActorUser,
		ActorID:        "user-1",
		OrganizationID: "org-1",
		Role:           string(store.RoleReleaseAdmin),
		ResourceType:   "operator",
		ResourceID:     resourceID,
		Action:         action,
		Status:         "succeeded",
	}
}

func TestVerificationPersistenceIncludesSignatureIdentity(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	record := &store.VerificationRecord{
		ID: uuid.NewString(), ArtifactDigest: "sha256:verification", PolicyVersion: "policy-v1",
		SignatureIdentity: "signature-identity", Status: store.VerificationTrusted,
		RootID: "root-1", KeyID: "key-1", RevocationEpoch: 7,
		Issuer: "issuer", Subject: "subject", Summary: "trusted",
		CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, st.Verifications().Create(ctx, record))

	got, err := st.Verifications().GetByDigestPolicyAndSignature(ctx, record.ArtifactDigest, record.PolicyVersion, record.SignatureIdentity)
	require.NoError(t, err)
	assert.Equal(t, record.SignatureIdentity, got.SignatureIdentity)
	assert.Equal(t, record.RootID, got.RootID)
	assert.Equal(t, record.KeyID, got.KeyID)
	assert.Equal(t, record.RevocationEpoch, got.RevocationEpoch)
	assert.False(t, got.CreatedAt.IsZero())

	_, err = st.Verifications().GetByDigestPolicyAndSignature(ctx, record.ArtifactDigest, record.PolicyVersion, "different-signature")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestTrustRootsGetActiveGraceWindow(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	expired := now.Add(-time.Hour)
	live := now.Add(24 * time.Hour)

	roots := []*store.TrustRoot{
		{ID: uuid.NewString(), Environment: "staging", KeyID: "key-active", PublicKeyPEM: "pem", Issuer: "active-ci", State: store.TrustRootActive, ValidFrom: now.Add(-2 * time.Hour)},
		{ID: uuid.NewString(), Environment: "staging", KeyID: "key-grace-live", PublicKeyPEM: "pem", Issuer: "grace-ci", State: store.TrustRootGrace, ValidFrom: now.Add(-time.Hour), GraceUntil: &live},
		// 无 grace_until 的 grace root 不是 live（与 trust.Root.Accepts 谓词一致）：服务端 Rotate 强制 grace root 必有 grace_until，缺失视为数据污染不参与验签。
		{ID: uuid.NewString(), Environment: "staging", KeyID: "key-grace-open", PublicKeyPEM: "pem", Issuer: "open-grace-ci", State: store.TrustRootGrace, ValidFrom: now.Add(-time.Hour)},
		{ID: uuid.NewString(), Environment: "staging", KeyID: "key-grace-expired", PublicKeyPEM: "pem", Issuer: "expired-ci", State: store.TrustRootGrace, ValidFrom: now.Add(-48 * time.Hour), GraceUntil: &expired},
		{ID: uuid.NewString(), Environment: "staging", KeyID: "key-future", PublicKeyPEM: "pem", Issuer: "future-ci", State: store.TrustRootActive, ValidFrom: now.Add(48 * time.Hour)},
		{ID: uuid.NewString(), Environment: "staging", KeyID: "key-pending", PublicKeyPEM: "pem", Issuer: "pending-ci", State: store.TrustRootPending, ValidFrom: now.Add(-time.Hour)},
		{ID: uuid.NewString(), Environment: "production", KeyID: "key-other-env", PublicKeyPEM: "pem", Issuer: "other-ci", State: store.TrustRootActive, ValidFrom: now.Add(-time.Hour)},
	}
	for _, root := range roots {
		require.NoError(t, st.TrustRoots().Create(ctx, root))
	}

	got, err := st.TrustRoots().GetActiveByEnvironment(ctx, "staging", now)
	require.NoError(t, err)
	var keys []string
	for _, root := range got {
		keys = append(keys, root.KeyID)
	}
	assert.ElementsMatch(t, []string{"key-active", "key-grace-live"}, keys)
}

func TestTrustRootCreateAndGet(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	now := time.Now().UTC()

	root := &store.TrustRoot{
		ID:           "root-001",
		Environment:  "staging",
		KeyID:        "k1",
		PublicKeyPEM: "-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEAtest\n-----END PUBLIC KEY-----",
		Issuer:       "release-manager-ci",
		State:        store.TrustRootActive,
		ValidFrom:    now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	require.NoError(t, st.TrustRoots().Create(ctx, root))

	got, err := st.TrustRoots().Get(ctx, "root-001")
	require.NoError(t, err)
	assert.Equal(t, root.ID, got.ID)
	assert.Equal(t, store.TrustRootActive, got.State)
	assert.Equal(t, "release-manager-ci", got.Issuer)
}

func TestTrustRootGetNotFound(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	_, err := st.TrustRoots().Get(ctx, "nonexistent")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestTrustRootListByEnvironment(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	now := time.Now().UTC()

	for _, id := range []string{"r1", "r2", "r3"} {
		require.NoError(t, st.TrustRoots().Create(ctx, &store.TrustRoot{
			ID: id, Environment: "staging", KeyID: id, Issuer: "ci-" + id,
			State: store.TrustRootActive, ValidFrom: now,
			CreatedAt: now, UpdatedAt: now,
		}))
	}
	// different environment
	require.NoError(t, st.TrustRoots().Create(ctx, &store.TrustRoot{
		ID: "r4", Environment: "production", KeyID: "k4", Issuer: "ci-prod",
		State: store.TrustRootActive, ValidFrom: now,
		CreatedAt: now, UpdatedAt: now,
	}))

	roots, err := st.TrustRoots().ListByEnvironment(ctx, "staging")
	require.NoError(t, err)
	assert.Len(t, roots, 3)

	roots, err = st.TrustRoots().ListByEnvironment(ctx, "production")
	require.NoError(t, err)
	assert.Len(t, roots, 1)
}

func TestTrustRootGetActiveByEnvironment(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	now := time.Now().UTC()
	past := now.Add(-1 * time.Hour)

	// Active root
	require.NoError(t, st.TrustRoots().Create(ctx, &store.TrustRoot{
		ID: "active-1", Environment: "staging", KeyID: "k1", Issuer: "ci-1",
		State: store.TrustRootActive, ValidFrom: past,
		CreatedAt: now, UpdatedAt: now,
	}))
	// Grace root
	graceUntil := now.Add(1 * time.Hour)
	require.NoError(t, st.TrustRoots().Create(ctx, &store.TrustRoot{
		ID: "grace-1", Environment: "staging", KeyID: "k2", Issuer: "ci-2",
		State: store.TrustRootGrace, ValidFrom: past, GraceUntil: &graceUntil,
		CreatedAt: now, UpdatedAt: now,
	}))
	// Retired root — should not appear
	require.NoError(t, st.TrustRoots().Create(ctx, &store.TrustRoot{
		ID: "retired-1", Environment: "staging", KeyID: "k3", Issuer: "ci-3",
		State: store.TrustRootRetired, ValidFrom: past,
		CreatedAt: now, UpdatedAt: now,
	}))

	active, err := st.TrustRoots().GetActiveByEnvironment(ctx, "staging", now)
	require.NoError(t, err)
	assert.Len(t, active, 2) // active + grace
}

func TestTrustRootUpdate(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	now := time.Now().UTC()

	require.NoError(t, st.TrustRoots().Create(ctx, &store.TrustRoot{
		ID: "root-upd", Environment: "staging", KeyID: "k1", Issuer: "ci-1",
		State: store.TrustRootPending, ValidFrom: now,
		CreatedAt: now, UpdatedAt: now,
	}))

	root, err := st.TrustRoots().Get(ctx, "root-upd")
	require.NoError(t, err)
	root.State = store.TrustRootActive
	require.NoError(t, st.TrustRoots().Update(ctx, root))

	got, err := st.TrustRoots().Get(ctx, "root-upd")
	require.NoError(t, err)
	assert.Equal(t, store.TrustRootActive, got.State)
}

func TestTrustRootBumpPolicy(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	// First bump initializes policy.
	ver, epoch, err := st.TrustRoots().BumpPolicy(ctx, "staging")
	require.NoError(t, err)
	assert.Equal(t, int64(1), ver)
	assert.Equal(t, int64(0), epoch)

	// Second bump increments.
	ver, _, err = st.TrustRoots().BumpPolicy(ctx, "staging")
	require.NoError(t, err)
	assert.Equal(t, int64(2), ver)
}

func TestTrustRootBumpRevocationEpoch(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	epoch, err := st.TrustRoots().BumpRevocationEpoch(ctx, "production")
	require.NoError(t, err)
	assert.Equal(t, int64(1), epoch)

	epoch, err = st.TrustRoots().BumpRevocationEpoch(ctx, "production")
	require.NoError(t, err)
	assert.Equal(t, int64(2), epoch)
}

func TestTrustRootGetPolicyDefault(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()

	// No policy row yet → returns defaults.
	meta, err := st.TrustRoots().GetPolicy(ctx, "unknown-env")
	require.NoError(t, err)
	assert.Equal(t, int64(1), meta.Version)
	assert.Equal(t, int64(0), meta.RevocationEpoch)
}

func TestTrustRootTransitionLiveRoot(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	now := time.Now().UTC()

	first := &store.TrustRoot{
		ID: "root-tl-1", Environment: "staging", KeyID: "key-tl-1", Issuer: "ci-1",
		State: store.TrustRootActive, ValidFrom: now,
		CreatedAt: now, UpdatedAt: now,
	}
	second := &store.TrustRoot{
		ID: "root-tl-2", Environment: "staging", KeyID: "key-tl-2", Issuer: "ci-2",
		State: store.TrustRootActive, ValidFrom: now,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, st.TrustRoots().Create(ctx, first))
	require.NoError(t, st.TrustRoots().Create(ctx, second))

	// Transition with a second live root present: state flips and the policy
	// version bumps in the same transaction.
	meta, err := st.TrustRoots().TransitionLiveRoot(ctx, first.ID, "staging", store.TrustRootRetired, nil, false)
	require.NoError(t, err)
	assert.Equal(t, int64(1), meta.Version)
	assert.Equal(t, int64(0), meta.RevocationEpoch)
	got, err := st.TrustRoots().Get(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, store.TrustRootRetired, got.State)

	// Revoking the last live root is forbidden and leaves no partial write:
	// the root stays live and the revocation epoch stays put.
	_, err = st.TrustRoots().TransitionLiveRoot(ctx, second.ID, "staging", store.TrustRootRevoked, &now, true)
	assert.ErrorIs(t, err, store.ErrLastRootRemovalForbidden)
	got, err = st.TrustRoots().Get(ctx, second.ID)
	require.NoError(t, err)
	assert.Equal(t, store.TrustRootActive, got.State)
	meta, err = st.TrustRoots().GetPolicy(ctx, "staging")
	require.NoError(t, err)
	assert.Equal(t, int64(0), meta.RevocationEpoch, "forbidden transition must not bump the epoch")

	// A non-live root cannot be transitioned.
	_, err = st.TrustRoots().TransitionLiveRoot(ctx, first.ID, "staging", store.TrustRootRetired, nil, false)
	assert.ErrorIs(t, err, store.ErrRootNotLive)
	// A missing root maps to ErrNotFound.
	_, err = st.TrustRoots().TransitionLiveRoot(ctx, "root-missing", "staging", store.TrustRootRetired, nil, false)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

// TestTrustRootTransitionConcurrentRemovalIsAtomicFile locks AC-043-03 on the
// store seam against a real on-disk SQLite database (WAL mode, production
// locking semantics): two racing removals must never leave the environment
// with zero live roots — exactly one wins, the other is rejected with
// ErrLastRootRemovalForbidden, and one live root remains.

func TestTrustRootTransitionConcurrentRemovalIsAtomicFile(t *testing.T) {
	st := setupFileStore(t)
	ctx := t.Context()
	now := time.Now().UTC()

	first := &store.TrustRoot{
		ID: "root-tl-f1", Environment: "staging", KeyID: "key-tl-f1", Issuer: "ci-1",
		State: store.TrustRootActive, ValidFrom: now,
		CreatedAt: now, UpdatedAt: now,
	}
	second := &store.TrustRoot{
		ID: "root-tl-f2", Environment: "staging", KeyID: "key-tl-f2", Issuer: "ci-2",
		State: store.TrustRootActive, ValidFrom: now,
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, st.TrustRoots().Create(ctx, first))
	require.NoError(t, st.TrustRoots().Create(ctx, second))

	const workers = 2
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for _, root := range []*store.TrustRoot{first, second} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			_, err := st.TrustRoots().TransitionLiveRoot(ctx, id, "staging", store.TrustRootRetired, nil, false)
			errs <- err
		}(root.ID)
	}
	close(start)
	wg.Wait()
	close(errs)

	var succeeded, forbidden int
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case assert.ErrorIs(t, err, store.ErrLastRootRemovalForbidden):
			forbidden++
		default:
			t.Fatalf("unexpected transition error: %v", err)
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, forbidden)

	live, err := st.TrustRoots().GetActiveByEnvironment(ctx, "staging", time.Now())
	require.NoError(t, err)
	assert.Len(t, live, 1, "exactly one live root must remain")
}

func TestInventoryQueryUsesDefaultPageSizeAndSyncLogTimestamp(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	syncedAt := time.Date(2026, time.July, 22, 9, 30, 0, 0, time.UTC)

	for i := range 51 {
		releaseName := fmt.Sprintf("release-%02d", i)
		require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
			CustomerID:      "customer-default-page",
			ClusterID:       "cluster-default-page",
			Namespace:       "apps",
			ReleaseName:     releaseName,
			InventoryStatus: store.InventoryActive,
			LastSyncID:      "sync-default-page",
			SnapshotVersion: 9,
		}))
	}
	inserted, err := st.Inventories().CreateSyncLog(ctx, &store.InventorySyncLog{
		SyncID:          "sync-default-page",
		CustomerID:      "customer-default-page",
		ClusterID:       "cluster-default-page",
		IsFullSnapshot:  true,
		AcceptedCount:   51,
		SnapshotVersion: 9,
		CreatedAt:       syncedAt,
	})
	require.NoError(t, err)
	require.True(t, inserted)

	page, err := st.Inventories().Query(ctx, store.InventoryQuery{
		CustomerID: "customer-default-page",
		ClusterID:  "cluster-default-page",
	})
	require.NoError(t, err)
	assert.Len(t, page.Items, 50)
	assert.Equal(t, 51, page.TotalCount)
	assert.NotEmpty(t, page.NextCursor)
	assert.Equal(t, syncedAt, page.LastSyncAt)

	next, err := st.Inventories().Query(ctx, store.InventoryQuery{
		CustomerID: "customer-default-page",
		ClusterID:  "cluster-default-page",
		Cursor:     page.NextCursor,
	})
	require.NoError(t, err)
	assert.Len(t, next.Items, 1)
}

func TestInventoryQueryPaginationFilteringAndConsistency(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	baseTime := time.Date(2026, time.July, 22, 8, 0, 0, 0, time.UTC)

	items := []*store.ReleaseInventory{
		{
			ReleaseDefinitionID: "definition-active",
			CustomerID:          "customer-1",
			ClusterID:           "cluster-1",
			Namespace:           "apps",
			ReleaseName:         "api",
			Chart:               "api",
			ChartVersion:        "1.0.0",
			Revision:            3,
			ValuesDigest:        "sha256:approved",
			InventoryStatus:     store.InventoryActive,
			LastSyncID:          "sync-1",
			SnapshotVersion:     7,
			CreatedAt:           baseTime,
		},
		{
			ReleaseDefinitionID: "definition-drifted",
			CustomerID:          "customer-1",
			ClusterID:           "cluster-1",
			Namespace:           "other",
			ReleaseName:         "api",
			Chart:               "api",
			ChartVersion:        "1.1.0",
			Revision:            4,
			ValuesDigest:        "sha256:actual",
			InventoryStatus:     store.InventoryActive,
			LastSyncID:          "sync-1",
			SnapshotVersion:     7,
			CreatedAt:           baseTime.Add(time.Second),
		},
		{
			CustomerID:      "customer-1",
			ClusterID:       "cluster-1",
			Namespace:       "system",
			ReleaseName:     "metrics",
			Chart:           "metrics",
			ChartVersion:    "2.0.0",
			Revision:        1,
			ValuesDigest:    "sha256:metrics",
			InventoryStatus: store.InventoryMissing,
			LastSyncID:      "sync-1",
			SnapshotVersion: 7,
			CreatedAt:       baseTime.Add(2 * time.Second),
		},
	}
	for _, item := range items {
		require.NoError(t, st.Inventories().Upsert(ctx, item))
	}

	activeDefinition := &store.ReleaseDefinition{
		ID: "definition-active", Name: "active", CustomerID: "customer-1", ClusterID: "cluster-1",
		Namespace: "apps", ReleaseName: "api", Status: store.DefStatusActive,
	}
	driftedDefinition := &store.ReleaseDefinition{
		ID: "definition-drifted", Name: "drifted", CustomerID: "customer-1", ClusterID: "cluster-1",
		Namespace: "other", ReleaseName: "api", Status: store.DefStatusActive,
	}
	require.NoError(t, st.Definitions().Create(ctx, activeDefinition, nil))
	require.NoError(t, st.Definitions().Create(ctx, driftedDefinition, nil))
	require.NoError(t, st.Values().Create(ctx, &store.ValuesRevision{
		ID: "values-active", ReleaseDefinitionID: activeDefinition.ID, Version: 1,
		Status: store.ValuesStatusApproved, CanonicalDocument: []byte(`{}`), Digest: "sha256:approved",
	}))
	require.NoError(t, st.Values().Create(ctx, &store.ValuesRevision{
		ID: "values-drifted", ReleaseDefinitionID: driftedDefinition.ID, Version: 1,
		Status: store.ValuesStatusApproved, CanonicalDocument: []byte(`{}`), Digest: "sha256:desired",
	}))

	page, err := st.Inventories().Query(ctx, store.InventoryQuery{
		CustomerID: "customer-1",
		ClusterID:  "cluster-1",
		NameSearch: "api",
		PageSize:   1,
	})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, store.InventoryActive, page.Items[0].InventoryStatus)
	assert.Equal(t, 2, page.TotalCount)
	assert.NotEmpty(t, page.NextCursor)

	next, err := st.Inventories().Query(ctx, store.InventoryQuery{
		CustomerID: "customer-1",
		ClusterID:  "cluster-1",
		NameSearch: "api",
		PageSize:   1,
		Cursor:     page.NextCursor,
	})
	require.NoError(t, err)
	require.Len(t, next.Items, 1)
	assert.Equal(t, store.InventoryOutOfSync, next.Items[0].InventoryStatus)
	assert.Empty(t, next.NextCursor)

	missing, err := st.Inventories().Query(ctx, store.InventoryQuery{
		CustomerID: "customer-1",
		ClusterID:  "cluster-1",
		Status:     store.InventoryMissing,
		PageSize:   50,
	})
	require.NoError(t, err)
	require.Len(t, missing.Items, 1)
	assert.Equal(t, "metrics", missing.Items[0].ReleaseName)

	require.NoError(t, st.Inventories().Upsert(ctx, &store.ReleaseInventory{
		CustomerID: "customer-1", ClusterID: "cluster-1", Namespace: "apps", ReleaseName: "worker",
		InventoryStatus: store.InventoryActive, LastSyncID: "sync-2", SnapshotVersion: 8,
	}))
	_, err = st.Inventories().Query(ctx, store.InventoryQuery{
		CustomerID: "customer-1",
		ClusterID:  "cluster-1",
		NameSearch: "api",
		PageSize:   1,
		Cursor:     page.NextCursor,
	})
	assert.ErrorIs(t, err, store.ErrInvalidCursor)
}

func TestTimelineRoundTripNewKinds(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	op := &store.Operation{
		ID:                  "timeline-roundtrip",
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      "timeline-roundtrip-key",
		RequestHash:         "request-hash",
		StateVersion:        1,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	ackData, err := json.Marshal(store.AckTimelineData{RequestID: "req-ack", AckStage: "persisted"})
	require.NoError(t, err)
	ack, err := st.Timeline().Append(ctx, &store.OperationTimelineEntry{
		OperationID: op.ID, Kind: string(store.TimelineEntryACK), Data: ackData,
	})
	require.NoError(t, err)

	progressData, err := json.Marshal(store.RolloutProgressTimelineData{WorkloadRef: "deployments/app/default", Ready: 2, Desired: 3})
	require.NoError(t, err)
	progress, err := st.Timeline().Append(ctx, &store.OperationTimelineEntry{
		OperationID: op.ID, Kind: string(store.TimelineEntryRolloutProgress), Data: progressData,
	})
	require.NoError(t, err)

	errorData, err := json.Marshal(store.ErrorTimelineData{RequestID: "req-err", ErrorCode: "helm_upgrade_failed", ErrorMessage: "sanitized summary"})
	require.NoError(t, err)
	errorEntry, err := st.Timeline().Append(ctx, &store.OperationTimelineEntry{
		OperationID: op.ID, Kind: string(store.TimelineEntryError), Data: errorData,
	})
	require.NoError(t, err)

	// Independent per-operation sequence allocation, strictly increasing.
	assert.Equal(t, int64(1), ack.Sequence)
	assert.Equal(t, int64(2), progress.Sequence)
	assert.Equal(t, int64(3), errorEntry.Sequence)

	entries, err := st.Timeline().List(ctx, op.ID, 0, 3)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, string(store.TimelineEntryACK), entries[0].Kind)
	assert.Equal(t, string(store.TimelineEntryRolloutProgress), entries[1].Kind)
	assert.Equal(t, string(store.TimelineEntryError), entries[2].Kind)

	var gotAck store.AckTimelineData
	require.NoError(t, json.Unmarshal(entries[0].Data, &gotAck))
	assert.Equal(t, store.AckTimelineData{RequestID: "req-ack", AckStage: "persisted"}, gotAck)

	var gotProgress store.RolloutProgressTimelineData
	require.NoError(t, json.Unmarshal(entries[1].Data, &gotProgress))
	assert.Equal(t, store.RolloutProgressTimelineData{WorkloadRef: "deployments/app/default", Ready: 2, Desired: 3}, gotProgress)

	var gotError store.ErrorTimelineData
	require.NoError(t, json.Unmarshal(entries[2].Data, &gotError))
	assert.Equal(t, store.ErrorTimelineData{RequestID: "req-err", ErrorCode: "helm_upgrade_failed", ErrorMessage: "sanitized summary"}, gotError)
}

func TestOutboxPersistAck_AtomicEntryAndIdempotency(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	op := &store.Operation{
		ID:                  "outbox-persist-ack",
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      "outbox-persist-ack-key",
		RequestHash:         "request-hash",
		StateVersion:        1,
	}
	require.NoError(t, st.Operations().Create(ctx, op))
	entry := pendingStoreEntry(t, st, op.ID, store.CommandDelivered)

	acked, err := st.Outbox().PersistAck(ctx, entry.ID)
	require.NoError(t, err)
	require.NotNil(t, acked)
	assert.Equal(t, string(store.TimelineEntryACK), acked.Kind)
	assert.Equal(t, int64(1), acked.Sequence)
	assert.Equal(t, op.ID, acked.OperationID)

	got, err := st.Outbox().Get(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.CommandPersisted, got.Status)
	require.NotNil(t, got.AckedAt)

	// Replayed ACK_PERSISTED must not write a second entry (AC-077-10).
	replay, err := st.Outbox().PersistAck(ctx, entry.ID)
	require.NoError(t, err)
	assert.Nil(t, replay)
	entries, err := st.Timeline().List(ctx, op.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestOutboxPersistAck_RollbackKeepsDelivered(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	// The outbox row references a ghost operation: the status UPDATE inside
	// the transaction succeeds but the timeline FK check fails, so the whole
	// transaction must roll back (AC-077-09) — no half-committed persisted state.
	entry := pendingStoreEntry(t, st, "ghost-operation", store.CommandDelivered)

	_, err := st.Outbox().PersistAck(ctx, entry.ID)
	require.Error(t, err)

	got, err := st.Outbox().Get(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, store.CommandDelivered, got.Status)
	assert.Nil(t, got.AckedAt)
}

func TestPersistAck_ConcurrentSequenceStrictlyIncreasing(t *testing.T) {
	st := setupFileStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	op := &store.Operation{
		ID:                  "persist-ack-concurrent",
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      "persist-ack-concurrent-key",
		RequestHash:         "request-hash",
		StateVersion:        1,
	}
	require.NoError(t, st.Operations().Create(ctx, op))
	e1 := pendingStoreEntry(t, st, op.ID, store.CommandDelivered)
	e2 := pendingStoreEntry(t, st, op.ID, store.CommandDelivered)

	// ACK_PERSISTED for two commands races with the terminal state transition
	// of the same operation: all three append timeline entries, and the shared
	// MAX(sequence)+1 allocation must yield a gap-free strictly increasing run
	// (AC-077-11).
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	wg.Add(3)
	go func() {
		defer wg.Done()
		_, err := st.Outbox().PersistAck(ctx, e1.ID)
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := st.Outbox().PersistAck(ctx, e2.ID)
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := st.Operations().Transition(ctx, op.ID, store.StatusSucceeded, op.StateVersion, "")
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	entries, err := st.Timeline().List(ctx, op.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	seen := map[int64]bool{}
	var prev int64
	for i, e := range entries {
		assert.False(t, seen[e.Sequence], "duplicate sequence %d", e.Sequence)
		seen[e.Sequence] = true
		if i > 0 {
			assert.Equal(t, prev+1, e.Sequence, "gap between sequences")
		}
		prev = e.Sequence
	}
	assert.Equal(t, int64(3), entries[2].Sequence)
}

// pendingStoreEntry inserts an outbox row with the given operation and status.
func pendingStoreEntry(t *testing.T, st *sqlitestore.Store, opID string, status store.CommandStatus) *store.OutboxEntry {
	t.Helper()
	e := &store.OutboxEntry{
		ID:            uuid.New().String(),
		CommandID:     uuid.New().String(),
		OperationID:   opID,
		OperationType: "INSTALL",
		OperatorID:    "op-1",
		Payload:       []byte(`{}`),
		Status:        status,
		MaxInFlight:   1,
		Sequence:      1,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	require.NoError(t, st.Outbox().Create(context.Background(), e))
	return e
}

func TestTransitionWritesSanitizedErrorEntry(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	op := &store.Operation{
		ID:                  "transition-error-entry",
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      "transition-error-entry-key",
		RequestHash:         "request-hash",
		StateVersion:        1,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	// AC-077-12: last_error carrying a Secret manifest / credential fragment
	// must surface as a sanitized summary with a stable code.
	sensitiveErr := `helm install failed {"code":"helm_install_failed"}: ` +
		`secret stringData: {"api_key": "sk-live-456"}` +
		` INSERT INTO audit (token) VALUES ('s3cret')`
	updated, err := st.Operations().Transition(ctx, op.ID, store.StatusFailed, op.StateVersion, sensitiveErr)
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, updated.Status)

	entries, err := st.Timeline().List(ctx, op.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, string(store.TimelineEntryStateTransition), entries[0].Kind)
	assert.Equal(t, string(store.TimelineEntryError), entries[1].Kind)
	assert.Equal(t, entries[0].Sequence+1, entries[1].Sequence)

	var errorData store.ErrorTimelineData
	require.NoError(t, json.Unmarshal(entries[1].Data, &errorData))
	assert.Equal(t, "helm_install_failed", errorData.ErrorCode)
	assert.NotContains(t, errorData.ErrorMessage, "sk-live-456")
	assert.NotContains(t, errorData.ErrorMessage, "s3cret")
	assert.Contains(t, errorData.ErrorMessage, "****REDACTED****")

	// The raw last_error column keeps its original semantics (only the entry is sanitized).
	persisted, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Contains(t, persisted.LastError, "sk-live-456")
}

func TestTransitionCancelledWritesNoErrorEntry(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	op := &store.Operation{
		ID:                  "transition-cancelled",
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      "transition-cancelled-key",
		RequestHash:         "request-hash",
		StateVersion:        1,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	// AC-077-03 negative: cancelled must not produce an ERROR entry even with last_error.
	// running → cancelling → cancelled (direct running→cancelled is not a legal transition).
	_, err := st.Operations().Transition(ctx, op.ID, store.StatusCancelling, op.StateVersion, "operator cancel requested")
	require.NoError(t, err)
	_, err = st.Operations().Transition(ctx, op.ID, store.StatusCancelled, 2, "operator cancelled")
	require.NoError(t, err)
	entries, err := st.Timeline().List(ctx, op.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, string(store.TimelineEntryStateTransition), entries[0].Kind)
	assert.Equal(t, string(store.TimelineEntryStateTransition), entries[1].Kind)
}

func TestTransitionSucceededWritesNoErrorEntry(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	op := &store.Operation{
		ID:                  "transition-succeeded",
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      "transition-succeeded-key",
		RequestHash:         "request-hash",
		StateVersion:        1,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	_, err := st.Operations().Transition(ctx, op.ID, store.StatusSucceeded, op.StateVersion, "")
	require.NoError(t, err)
	entries, err := st.Timeline().List(ctx, op.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestFinalizeUpgradeFailedWritesSanitizedErrorEntry(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	op := &store.Operation{
		ID:                  "finalize-upgrade-error",
		OperationType:       store.OperationUpgrade,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      "finalize-upgrade-error-key",
		RequestHash:         "request-hash",
		StateVersion:        2,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	sensitiveErr := `{"code":"rollout_timeout"}: timeout waiting for Deployment rollback (token=abc987)`
	err := st.UpgradeResults().FinalizeUpgrade(ctx, &store.UpgradeTerminalInput{
		OperationID: op.ID, ExpectedStateVersion: op.StateVersion, Status: store.StatusFailed,
		LastError: sensitiveErr, ResultPayload: []byte(`{"upgrade":"failed"}`),
	})
	require.NoError(t, err)

	entries, err := st.Timeline().List(ctx, op.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, string(store.TimelineEntryError), entries[1].Kind)

	var errorData store.ErrorTimelineData
	require.NoError(t, json.Unmarshal(entries[1].Data, &errorData))
	assert.Equal(t, "rollout_timeout", errorData.ErrorCode)
	assert.NotContains(t, errorData.ErrorMessage, "abc987")
}

func TestFinalizeUpgradeFallbackCode(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	op := &store.Operation{
		ID:                  "finalize-upgrade-fallback",
		OperationType:       store.OperationUpgrade,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      "finalize-upgrade-fallback-key",
		RequestHash:         "request-hash",
		StateVersion:        2,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	err := st.UpgradeResults().FinalizeUpgrade(ctx, &store.UpgradeTerminalInput{
		OperationID: op.ID, ExpectedStateVersion: op.StateVersion, Status: store.StatusTimeout,
		LastError: "execution window exhausted with unstructured payload", ResultPayload: []byte(`{}`),
	})
	require.NoError(t, err)

	entries, err := st.Timeline().List(ctx, op.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	var errorData store.ErrorTimelineData
	require.NoError(t, json.Unmarshal(entries[1].Data, &errorData))
	assert.Equal(t, "operation_failed", errorData.ErrorCode)
}
