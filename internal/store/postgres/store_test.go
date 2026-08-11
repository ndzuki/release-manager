//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/config"
	"github.com/ndzuki/release-manager/internal/postgres"
	"github.com/ndzuki/release-manager/internal/store"
	postgresstore "github.com/ndzuki/release-manager/internal/store/postgres"
	"gorm.io/gorm"
)

func setupStore(t *testing.T) *postgresstore.Store {
	t.Helper()
	baseDSN := os.Getenv("POSTGRES_TEST_DSN")
	if baseDSN == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := t.Context()
	dsn := postgresStoreTestSchema(ctx, t, baseDSN)
	database, err := postgres.Open(ctx, config.DatabaseConfig{DSN: dsn})
	require.NoError(t, err)
	migrationFS, err := postgres.LoadMigrationFS("../../../migrations")
	require.NoError(t, err)
	require.NoError(t, postgres.RunMigrations(ctx, database.SQLDB(), migrationFS))
	st, err := postgresstore.New(database.SQLDB(), database.GORM())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return st
}

func postgresStoreTestSchema(ctx context.Context, t *testing.T, baseDSN string) string {
	t.Helper()
	schema := "task070_store_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	db, err := sql.Open("pgx", baseDSN)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA %q`, schema)) //nolint:gosec // schema is generated from a UUID.
	require.NoError(t, err)
	cleanupCtx := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		_, dropErr := db.ExecContext(cleanupCtx, fmt.Sprintf(`DROP SCHEMA %q CASCADE`, schema)) //nolint:gosec // schema is generated from a UUID.
		require.NoError(t, dropErr)
		require.NoError(t, db.Close())
	})
	parsed, err := url.Parse(baseDSN)
	require.NoError(t, err)
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
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
	assert.NoError(t, rows.Err())

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
	require.NoError(t, st.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM operation_events WHERE operation_id = $1`, op.ID).Scan(&eventCount))
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

func TestOperationTargetRevisionPersistence(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	op := &store.Operation{
		ID:                  uuid.NewString(),
		OperationType:       store.OperationRollback,
		Status:              store.StatusPending,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      uuid.NewString(),
		ExpectedRevision:    4,
		TargetRevision:      2,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	got, err := st.Operations().Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, 4, got.ExpectedRevision)
	assert.Equal(t, 2, got.TargetRevision)
}

func TestOperationTransition_TerminalAt(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	op := &store.Operation{
		ID:                  uuid.NewString(),
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      uuid.NewString(),
	}
	require.NoError(t, st.Operations().Create(ctx, op))
	require.NoError(t, st.PreflightLifecycles().Create(ctx, &store.PreflightLifecycle{
		OperationID: &op.ID,
		Stages:      []byte(`[{"stage":"control-plane","status":"passed"}]`),
		Overall:     "passed",
	}))

	updated, err := st.Operations().Transition(ctx, op.ID, store.StatusSucceeded, op.StateVersion, "")
	require.NoError(t, err)
	require.NotNil(t, updated.TerminalAt)

	var operationTerminalAt time.Time
	require.NoError(t, st.SQLDB().QueryRowContext(ctx,
		`SELECT terminal_at FROM operations WHERE id = $1`, op.ID,
	).Scan(&operationTerminalAt))
	assert.False(t, operationTerminalAt.IsZero())

	var lifecycleTerminalAt time.Time
	require.NoError(t, st.SQLDB().QueryRowContext(ctx,
		`SELECT operation_terminal_at FROM preflight_lifecycles WHERE operation_id = $1`, op.ID,
	).Scan(&lifecycleTerminalAt))
	assert.Equal(t, operationTerminalAt, lifecycleTerminalAt)
}

func TestOperationTransition_TerminalAtWithoutLifecycle(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	op := &store.Operation{
		ID:                  uuid.NewString(),
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      uuid.NewString(),
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	updated, err := st.Operations().Transition(ctx, op.ID, store.StatusSucceeded, op.StateVersion, "")
	require.NoError(t, err)
	require.NotNil(t, updated.TerminalAt)

	var count int
	require.NoError(t, st.SQLDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM preflight_lifecycles WHERE operation_id = $1`, op.ID,
	).Scan(&count))
	assert.Zero(t, count)
}

func TestIdentityAndAuditAccessors(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	user := &store.User{ID: "user-1", Username: "alice", PasswordHash: "hash"}
	require.NoError(t, st.Users().Create(ctx, user))
	org := &store.Organization{ID: "org-1", Name: "Engineering"}
	require.NoError(t, st.Organizations().Create(ctx, org))
	member := &store.OrganizationMember{OrgID: org.ID, UserID: user.ID, Role: store.RoleReleaseAdmin}
	require.NoError(t, st.OrgMembers().Create(ctx, member))
	binding := &store.OrgCustomerBinding{ID: "binding-1", OrgID: org.ID, CustomerID: "customer-1"}
	require.NoError(t, st.Bindings().Create(ctx, binding))
	session := &store.AuthSession{
		ID: "session-1", UserID: user.ID, TokenFamily: "family-1",
		RefreshTokenHash: "refresh-hash", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	require.NoError(t, st.AuthSessions().Create(ctx, session))
	event := &store.AuditEvent{
		ID: "audit-1", ActorKind: store.AuditActorUser, ActorID: user.ID,
		ResourceType: "organization", ResourceID: org.ID, Action: "create", Status: "succeeded",
	}
	require.NoError(t, st.AuditEvents().Create(ctx, event))

	gotUser, err := st.Users().Get(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, user.Username, gotUser.Username)
	active, err := st.AuthSessions().HasActiveByUserID(ctx, user.ID)
	require.NoError(t, err)
	assert.True(t, active)
	gotBinding, err := st.Bindings().GetByOrgAndCustomer(ctx, org.ID, binding.CustomerID)
	require.NoError(t, err)
	assert.Equal(t, store.BindingActive, gotBinding.Status)
	events, err := st.AuditEvents().ListByResource(ctx, "organization", org.ID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, user.ID, events[0].ActorID)
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

func createTestDefinition(t *testing.T, st *postgresstore.Store) *store.ReleaseDefinition {
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

func TestValuesApprovalOptimisticLock(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)

	vr := &store.ValuesRevision{
		ID:                  uuid.New().String(),
		ReleaseDefinitionID: def.ID,
		Revision:            1,
		StateVersion:        1,
		Status:              store.ValuesStatusDraft,
		Values:              []byte(`{"key":"v1"}`),
		Digest:              "sha256:abc",
		CreatedByUserID:     "creator-1",
	}
	require.NoError(t, st.Values().Create(ctx, vr))

	submitted, err := st.ValuesApproval().Submit(ctx, store.ValuesApprovalCommand{
		RevisionID: vr.ID, ExpectedStateVersion: 1, ActorUserID: "creator-1", Authorized: true,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2, submitted.Revision.StateVersion)

	_, err = st.ValuesApproval().Approve(ctx, store.ValuesApprovalCommand{
		RevisionID: vr.ID, ExpectedStateVersion: 1, ActorUserID: "approver-1", Authorized: true,
	})
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

func TestVerificationPersistenceIncludesTrustProvenance(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	record := &store.VerificationRecord{
		ID: uuid.NewString(), ArtifactDigest: "sha256:verification", PolicyVersion: "policy-v1",
		SignatureIdentity: "signature-identity", Status: store.VerificationTrusted,
		RootID: "root-1", KeyID: "key-1", RevocationEpoch: 7,
		Issuer: "issuer", Subject: "subject", Summary: "trusted",
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

func TestCandidateArtifactDuplicateRefreshesIdentity(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	first := &store.CandidateArtifact{ArtifactType: store.ArtifactImage, Ref: "registry/app:v1", Digest: "sha256:candidate-refresh"}
	require.NoError(t, st.CandidateArtifacts().Create(ctx, first))

	second := &store.CandidateArtifact{ArtifactType: store.ArtifactImage, Ref: "registry/app:v2", Digest: first.Digest}
	require.NoError(t, st.CandidateArtifacts().Create(ctx, second))
	assert.Equal(t, first.ID, second.ID)

	var ref string
	require.NoError(t, st.SQLDB().QueryRowContext(ctx,
		`SELECT ref FROM candidate_artifacts WHERE id = $1`, first.ID,
	).Scan(&ref))
	assert.Equal(t, second.Ref, ref)
}

func TestTrustAndVulnerabilityAccessors(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	root := &store.TrustRoot{
		ID: uuid.NewString(), Environment: "staging", KeyID: "key-staging", PublicKeyPEM: "pem",
		Issuer: "issuer", SubjectPattern: "subject-*", State: store.TrustRootActive, ValidFrom: now.Add(-time.Hour),
	}
	require.NoError(t, st.TrustRoots().Create(ctx, root))
	gotRoot, err := st.TrustRoots().Get(ctx, root.ID)
	require.NoError(t, err)
	assert.Equal(t, root.KeyID, gotRoot.KeyID)
	version, epoch, err := st.TrustRoots().BumpPolicy(ctx, root.Environment)
	require.NoError(t, err)
	assert.EqualValues(t, 1, version)
	assert.EqualValues(t, 0, epoch)

	scan := &store.ScanResultRecord{
		ID: uuid.NewString(), ArtifactDigest: "sha256:scan", SBOMRef: "sbom", Scanner: "trivy",
		ResultVersion: "v1", SeverityJSON: []byte(`{"critical":0}`), FindingsJSON: []byte(`[]`),
	}
	require.NoError(t, st.ScanResults().Create(ctx, scan))
	gotScan, err := st.ScanResults().GetLatest(ctx, scan.ArtifactDigest, scan.Scanner)
	require.NoError(t, err)
	assert.JSONEq(t, string(scan.SeverityJSON), string(gotScan.SeverityJSON))

	exception := &store.VulnerabilityExceptionRecord{
		ID: uuid.NewString(), FindingID: "CVE-TEST", ArtifactDigest: scan.ArtifactDigest,
		Actor: "platform-admin", Reason: "temporary", ExpiresAt: now.Add(time.Hour),
	}
	require.NoError(t, st.VulnerabilityExceptions().Create(ctx, exception))
	gotException, err := st.VulnerabilityExceptions().Get(ctx, exception.ID)
	require.NoError(t, err)
	assert.Equal(t, exception.Reason, gotException.Reason)
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

func TestAuditExportAtomicPersistence(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	export := &store.AuditExport{ID: uuid.NewString(), OrganizationID: "org-audit", Since: now.Add(-time.Hour), Until: now}
	event := &store.AuditEvent{
		ID: uuid.NewString(), ActorKind: store.AuditActorUser, ActorID: "user-audit", OrganizationID: export.OrganizationID,
		ResourceType: "audit_export", ResourceID: export.ID, Action: "create", Status: "succeeded",
	}
	require.NoError(t, st.AuditExports().CreateWithEvent(ctx, export, event))

	var exportCount int
	require.NoError(t, st.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_exports WHERE id = $1`, export.ID).Scan(&exportCount))
	assert.Equal(t, 1, exportCount)
	gotEvent, err := st.AuditEvents().GetByID(ctx, event.ID)
	require.NoError(t, err)
	assert.Equal(t, export.ID, gotEvent.ResourceID)
}

func TestAuthSessionLifecycle(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	user := &store.User{ID: uuid.NewString(), Username: "auth-session-user", PasswordHash: "hash"}
	require.NoError(t, st.Users().Create(ctx, user))
	active := &store.AuthSession{
		ID: uuid.NewString(), UserID: user.ID, TokenFamily: "family-active", RefreshTokenHash: "refresh-active",
		ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}
	expired := &store.AuthSession{
		ID: uuid.NewString(), UserID: user.ID, TokenFamily: "family-expired", RefreshTokenHash: "refresh-expired",
		ExpiresAt: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour),
	}
	require.NoError(t, st.AuthSessions().Create(ctx, active))
	require.NoError(t, st.AuthSessions().Create(ctx, expired))

	got, err := st.AuthSessions().GetByRefreshHash(ctx, active.RefreshTokenHash)
	require.NoError(t, err)
	assert.Equal(t, active.ID, got.ID)
	hasActive, err := st.AuthSessions().HasActiveByUserID(ctx, user.ID)
	require.NoError(t, err)
	assert.True(t, hasActive)
	require.NoError(t, st.AuthSessions().RevokeFamily(ctx, active.TokenFamily))
	hasActive, err = st.AuthSessions().HasActiveByUserID(ctx, user.ID)
	require.NoError(t, err)
	assert.False(t, hasActive)
	deleted, err := st.AuthSessions().DeleteExpired(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	_, err = st.AuthSessions().Get(ctx, expired.ID)
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestPreflightLifecycleRetention(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	exploratory := &store.PreflightLifecycle{Stages: []byte(`[]`), Overall: "passed", CreatedAt: now.Add(-8 * 24 * time.Hour)}
	require.NoError(t, st.PreflightLifecycles().Create(ctx, exploratory))

	definition := createTestDefinition(t, st)
	operationID := uuid.NewString()
	require.NoError(t, st.Operations().Create(ctx, &store.Operation{
		ID: operationID, OperationType: store.OperationInstall, Status: store.StatusRunning,
		ReleaseDefinitionID: definition.ID, IdempotencyKey: uuid.NewString(), RequestHash: "preflight-retention",
	}))
	linked := &store.PreflightLifecycle{OperationID: &operationID, Stages: []byte(`[]`), Overall: "passed", CreatedAt: now.Add(-8 * 24 * time.Hour)}
	require.NoError(t, st.PreflightLifecycles().Create(ctx, linked))
	require.NoError(t, st.PreflightLifecycles().SetOperationTerminal(ctx, operationID, now.Add(-8*24*time.Hour)))
	require.NoError(t, st.PreflightLifecycles().SetOperationTerminal(ctx, operationID, now))

	deleted, err := st.PreflightLifecycles().DeleteExpired(ctx, 7*24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)
}

func TestOperationCreationUnitOfWorkRollsBackGORMAndRawSQL(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	sentinel := assert.AnError
	err := postgres.OperationCreationUnitOfWork(ctx, st.GORM(), func(tx *gorm.DB, sqlTx *sql.Tx) error {
		if err := tx.WithContext(ctx).Exec(
			`INSERT INTO customers (id, name, slug, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			"uow-gorm", "UoW GORM", "uow-gorm", string(store.CustomerActive), time.Now().UTC(), time.Now().UTC(),
		).Error; err != nil {
			return err
		}
		if _, err := sqlTx.ExecContext(ctx,
			`INSERT INTO customers (id, name, slug, status, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6)`,
			"uow-raw", "UoW Raw", "uow-raw", string(store.CustomerActive), time.Now().UTC(), time.Now().UTC(),
		); err != nil {
			return err
		}
		return sentinel
	})
	assert.ErrorIs(t, err, sentinel)
	for _, id := range []string{"uow-gorm", "uow-raw"} {
		var count int
		require.NoError(t, st.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM customers WHERE id = $1`, id).Scan(&count))
		assert.Zero(t, count)
	}
}

func TestBundleStoreLifecycle(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	bundle := &store.ReleaseBundle{
		ID: uuid.NewString(), Name: "PostgreSQL Bundle", DigestAlg: "sha256", DigestValue: uuid.NewString(),
		Status: store.BundleValidated, Images: []store.BundleImage{{Ref: "registry/app:v1", Digest: "sha256:image", ValuesPath: "image.repository"}},
	}
	require.NoError(t, st.Bundles().Create(ctx, bundle))
	got, err := st.Bundles().GetByDigest(ctx, bundle.DigestAlg, bundle.DigestValue)
	require.NoError(t, err)
	assert.Equal(t, bundle.ID, got.ID)
	assert.Equal(t, bundle.Images, got.Images)

	archived, err := st.Bundles().Archive(ctx, []string{bundle.ID})
	require.NoError(t, err)
	assert.Equal(t, int64(1), archived)
	require.NoError(t, st.Bundles().Unarchive(ctx, bundle.ID))
	got, err = st.Bundles().Get(ctx, bundle.ID)
	require.NoError(t, err)
	assert.Equal(t, store.BundleValidated, got.Status)
}

func TestNotificationStoreLifecycle(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	job := &store.NotificationJob{
		ID: uuid.NewString(), OperationID: uuid.NewString(), Channel: store.NotificationChannelWebhook,
		Recipient: "https://example.invalid/hook", Status: store.NotificationPending, MaxRetries: 3,
		Metadata: map[string]string{"source": "postgres-test"}, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, st.Notifications().Create(ctx, job))
	pending, err := st.Notifications().GetPending(ctx, now, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	assert.Equal(t, job.ID, pending[0].ID)

	claimed, err := st.Notifications().ClaimNext(ctx, now)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, store.NotificationSending, claimed.Status)
	assert.Equal(t, 1, claimed.Attempts)
	require.NoError(t, st.Notifications().MarkDeadLetter(ctx, job.ID, "delivery_failed", "test failure"))
	deadLetter, err := st.Notifications().Get(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, store.NotificationDeadLetter, deadLetter.Status)
	assert.NotNil(t, deadLetter.DeadLetterAt)
	deleted, err := st.Notifications().DeleteDeadLetterBefore(ctx, time.Now().UTC().Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
}

func TestCustomerEventStoreCreate(t *testing.T) {
	st := setupStore(t)
	ctx := t.Context()
	customer := &store.Customer{ID: uuid.NewString(), Name: "Event Customer", Slug: "event-customer-" + uuid.NewString()}
	require.NoError(t, st.Customers().Create(ctx, customer))
	event := &store.CustomerEvent{ID: uuid.NewString(), CustomerID: customer.ID, EventType: "customer.created"}
	require.NoError(t, st.CustomerEvents().Create(ctx, event))

	var eventType string
	var createdAt time.Time
	require.NoError(t, st.SQLDB().QueryRowContext(ctx,
		`SELECT event_type, created_at FROM customer_events WHERE id = $1`, event.ID,
	).Scan(&eventType, &createdAt))
	assert.Equal(t, event.EventType, eventType)
	assert.False(t, createdAt.IsZero())
}

func TestAdvisoryLockCompetesAcrossConnections(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	const key int64 = 70070
	first, acquired, err := postgres.TryAcquireAdvisoryLock(ctx, st.SQLDB(), key)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, first.Unlock()) })

	second, acquired, err := postgres.TryAcquireAdvisoryLock(ctx, st.SQLDB(), key)
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Nil(t, second)
	require.NoError(t, first.Unlock())

	third, acquired, err := postgres.TryAcquireAdvisoryLock(ctx, st.SQLDB(), key)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, third.Unlock())
}
