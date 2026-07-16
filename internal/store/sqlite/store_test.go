package sqlite_test

import (
	"context"
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
	require.NoError(t, st.Clusters().Update(ctx, cl))

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
	require.NoError(t, st.Definitions().Create(ctx, def))
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

func TestValuesRevisionUpdateOptimisticLock(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	vr := &store.ValuesRevision{
		ID:                  uuid.New().String(),
		ReleaseDefinitionID: def.ID,
		Revision:            1,
		Status:              store.ValuesStatusDraft,
		Values:              []byte(`{"key":"v1"}`),
		Digest:              "sha256:abc",
		ParentRevisionID:    "parent-rev-1",
	}
	require.NoError(t, st.Values().Create(ctx, vr))

	// Update with correct parent — should succeed
	vr.Status = store.ValuesStatusApproved
	err := st.Values().Update(ctx, vr, "parent-rev-1")
	require.NoError(t, err)

	// Verify
	got, err := st.Values().Get(ctx, vr.ID)
	require.NoError(t, err)
	assert.Equal(t, store.ValuesStatusApproved, got.Status)

	// Update with wrong parent — should get optimistic lock error
	vr.Status = store.ValuesStatusRejected
	err = st.Values().Update(ctx, vr, "wrong-parent")
	assert.ErrorIs(t, err, store.ErrOptimisticLock)
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
