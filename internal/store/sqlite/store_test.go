package sqlite_test

import (
	"context"
	"fmt"
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
	st, err := sqlitestore.Open("file::memory:?cache=shared")
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
	require.NoError(t, st.Customers().Update(ctx, c))

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
	assert.False(t, got.Used)

	// Mark used.
	require.NoError(t, st.EnrollmentTokens().MarkUsed(ctx, tok.ID, "op-001"))

	got, err = st.EnrollmentTokens().GetByToken(ctx, "test-token-abc")
	require.NoError(t, err)
	assert.True(t, got.Used)
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
		Revision:            1,
		Status:              store.ValuesStatusDraft,
		Values:              []byte(`{"key":"value"}`),
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
	assert.Equal(t, 1, got.Revision)
}

func TestValuesRevisionGetByDigest(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	vr := &store.ValuesRevision{
		ID:                  uuid.New().String(),
		ReleaseDefinitionID: def.ID,
		Revision:            1,
		Status:              store.ValuesStatusDraft,
		Values:              []byte(`{}`),
		Digest:              "auth0:deadbeef",
	}
	require.NoError(t, st.Values().Create(ctx, vr))

	got, err := st.Values().GetByDigest(ctx, def.ID, "auth0:deadbeef")
	require.NoError(t, err)
	assert.Equal(t, vr.ID, got.ID)
}

func TestValuesRevisionGetNextRevisionNumber(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	// First revision
	n, err := st.Values().GetNextRevisionNumber(ctx, def.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	vr := &store.ValuesRevision{
		ID:                  uuid.New().String(),
		ReleaseDefinitionID: def.ID,
		Revision:            1,
		Status:              store.ValuesStatusDraft,
		Values:              []byte(`{}`),
		Digest:              "sha256:a",
	}
	require.NoError(t, st.Values().Create(ctx, vr))

	n, err = st.Values().GetNextRevisionNumber(ctx, def.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
}

func TestValuesRevisionList(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	for i := 1; i <= 3; i++ {
		vr := &store.ValuesRevision{
			ID:                  uuid.New().String(),
			ReleaseDefinitionID: def.ID,
			Revision:            i,
			Status:              store.ValuesStatusDraft,
			Values:              []byte(`{}`),
			Digest:              "sha256:" + string(rune('a'+i-1)),
		}
		require.NoError(t, st.Values().Create(ctx, vr))
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
		Revision:            1,
		StateVersion:        1,
		Status:              store.ValuesStatusApproved,
		Values:              []byte(`{"key":"old"}`),
		Digest:              "sha256:old",
		CreatedByUserID:     "creator-old",
	}
	next := &store.ValuesRevision{
		ID:                  uuid.New().String(),
		ReleaseDefinitionID: def.ID,
		Revision:            2,
		StateVersion:        1,
		Status:              store.ValuesStatusPendingApproval,
		Values:              []byte(`{"key":"new"}`),
		Digest:              "sha256:new",
		CreatedByUserID:     "creator-new",
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
		ID: uuid.New().String(), ReleaseDefinitionID: def.ID, Revision: 1,
		StateVersion: 1, Status: store.ValuesStatusPendingApproval,
		Values: []byte(`{"key":"value"}`), Digest: "sha256:unauthorized",
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
		Revision:            1,
		StateVersion:        1,
		Status:              store.ValuesStatusPendingApproval,
		Values:              []byte(`{"key":"approve"}`),
		Digest:              "sha256:approve",
		CreatedByUserID:     "creator-approve",
	}
	rejection := &store.ValuesRevision{
		ID:                  uuid.New().String(),
		ReleaseDefinitionID: def.ID,
		Revision:            2,
		StateVersion:        1,
		Status:              store.ValuesStatusDraft,
		Values:              []byte(`{"key":"reject"}`),
		Digest:              "sha256:reject",
		CreatedByUserID:     "creator-reject",
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
		ID: "values-active", ReleaseDefinitionID: activeDefinition.ID, Revision: 1,
		Status: store.ValuesStatusApproved, Values: []byte(`{}`), Digest: "sha256:approved",
	}))
	require.NoError(t, st.Values().Create(ctx, &store.ValuesRevision{
		ID: "values-drifted", ReleaseDefinitionID: driftedDefinition.ID, Revision: 1,
		Status: store.ValuesStatusApproved, Values: []byte(`{}`), Digest: "sha256:desired",
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
