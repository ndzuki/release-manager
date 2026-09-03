// Package sqlite provides a SQLite-backed implementation of the store interfaces.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	modernc "modernc.org/sqlite"

	"github.com/ndzuki/release-manager/internal/store"
)

// Store implements store.Store backed by SQLite.
type Store struct {
	db               *sql.DB
	ops              *operationStore
	operationEvents  *operationEventStore
	timeline         *timelineStore
	defs             *definitionStore
	vals             *valuesStore
	valuesApproval   *valuesApprovalStore
	customers        *customerStore
	clusters         *clusterStore
	tokens           *enrollmentTokenStore
	operators        *operatorStore
	sessions         *sessionStore
	outbox           *outboxStore
	users            *userStore
	authSess         *authSessionStore
	orgs             *organizationStore
	orgMembers       *organizationMemberStore
	bindings         *bindingStore
	audit            *auditEventStore
	notif            *notificationStore
	bundles          *bundleStore
	verifs           *verificationStore
	scanResults      *scanResultStore
	vulnExceptions   *vulnerabilityExceptionStore
	trustRoots       *trustRootStore
	routes           *clusterRouteStore
	invs             *inventoryStore
	syncRequests     *inventorySyncRequestStore
	custEvents       *customerEventStore
	customerCreates  *customerBindingCreateStore
	defEvents        *definitionEventStore
	preflight        *preflightStore
	candidateArts    *candidateArtifactStore
	preflightCycles  *preflightLifecycleStore
	auditExports     *auditExportStore
	executionResults *operationExecutionResultStore
	rollouts         *rolloutTrackingStore
	emergencyIntents *emergencyIntentStore
	convergenceTasks *convergenceTaskStore
	emergencyConfig  *emergencyConfigStore
	valuesLifecycle  *valuesLifecycleStore
	prepareSessions  *prepareSessionStore
	authorization    *authorizationStore
	idem             *idempotencyStore
}

// Open creates a new SQLite-backed Store, running migrations on the database.
// The DSN must be a valid modernc.org/sqlite connection string.
func Open(dsn string) (*Store, error) {
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	db, err := sql.Open("sqlite", dsn+separator+"_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}

	// WAL mode for better concurrent read performance.
	if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite pragma journal_mode: %w", err)
	}
	// Enable foreign keys.
	if _, err := db.ExecContext(context.Background(), "PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite pragma foreign_keys: %w", err)
	}

	if err := migrate(db, dsn); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite migrate: %w", err)
	}

	s := &Store{db: db}
	s.ops = &operationStore{db: db}
	s.operationEvents = &operationEventStore{db: db}
	s.timeline = &timelineStore{db: db}
	s.defs = &definitionStore{db: db}
	s.defEvents = &definitionEventStore{db: db}
	s.preflight = &preflightStore{db: db}
	s.vals = &valuesStore{db: db}
	s.valuesApproval = &valuesApprovalStore{db: db}
	s.customers = &customerStore{db: db}
	s.clusters = &clusterStore{db: db}
	s.tokens = &enrollmentTokenStore{db: db}
	s.operators = &operatorStore{db: db}
	s.sessions = &sessionStore{db: db}
	s.outbox = &outboxStore{db: db}
	s.users = &userStore{db: db}
	s.authSess = &authSessionStore{db: db}
	s.orgs = &organizationStore{db: db}
	s.orgMembers = &organizationMemberStore{db: db}
	s.bindings = &bindingStore{db: db}
	s.notif = &notificationStore{db: db}
	s.idem = &idempotencyStore{db: db}
	s.audit = &auditEventStore{db: db}
	s.trustRoots = &trustRootStore{db: db}
	s.vulnExceptions = &vulnerabilityExceptionStore{db: db}
	s.scanResults = &scanResultStore{db: db}
	s.auditExports = &auditExportStore{db: db}
	s.bundles = &bundleStore{db: db}
	s.invs = &inventoryStore{db: db}
	s.verifs = &verificationStore{db: db}
	s.syncRequests = &inventorySyncRequestStore{db: db}
	s.candidateArts = &candidateArtifactStore{db: db}
	s.custEvents = &customerEventStore{db: db}
	s.routes = &clusterRouteStore{db: db}
	s.emergencyIntents = &emergencyIntentStore{db: db}
	s.convergenceTasks = &convergenceTaskStore{db: db}
	s.emergencyConfig = &emergencyConfigStore{db: db}
	s.valuesLifecycle = &valuesLifecycleStore{db: db}
	s.prepareSessions = &prepareSessionStore{db: db}
	s.authorization = &authorizationStore{db: db}
	s.preflightCycles = &preflightLifecycleStore{db: db}
	s.executionResults = &operationExecutionResultStore{db: db}
	s.rollouts = &rolloutTrackingStore{db: db}
	s.customerCreates = &customerBindingCreateStore{db: db}
	return s, nil
}

// Operations returns the OperationStore.
func (s *Store) Operations() store.OperationStore { return s.ops }

// OperationEvents returns the operation state event store.
func (s *Store) OperationEvents() store.OperationEventStore { return s.operationEvents }

// Timeline returns the ordered Operation timeline store.
func (s *Store) Timeline() store.TimelineStore { return s.timeline }

// ExecutionResults returns typed operation result records.
func (s *Store) ExecutionResults() store.OperationExecutionResultStore { return s.executionResults }

// RolloutTrackings returns rollout observation records.
func (s *Store) RolloutTrackings() store.RolloutTrackingStore { return s.rollouts }

// UpgradeResults returns the atomic upgrade terminal writer.
func (s *Store) UpgradeResults() store.UpgradeResultStore { return s.ops }

// Customers returns the CustomerStore.
func (s *Store) Customers() store.CustomerStore { return s.customers }

// CustomerCreates returns the atomic customer+org-binding creation module.
func (s *Store) CustomerCreates() store.CustomerBindingCreateStore { return s.customerCreates }

// Clusters returns the ClusterStore.
func (s *Store) Clusters() store.ClusterStore { return s.clusters }

// OperatorManagement returns the atomic Operator management store.
func (s *Store) OperatorManagement() store.OperatorManagementStore {
	return &operatorManagementStore{db: s.db}
}

// OperatorLifecycle returns the REQ-015 certificate and scope-disable
// transaction store.
func (s *Store) OperatorLifecycle() store.OperatorLifecycleStore {
	return &operatorLifecycleStore{db: s.db}
}

// EnrollmentTokens returns the EnrollmentTokenStore.
func (s *Store) EnrollmentTokens() store.EnrollmentTokenStore { return s.tokens }

// Operators returns the OperatorStore.
func (s *Store) Operators() store.OperatorStore { return s.operators }

// Sessions returns the SessionStore.
func (s *Store) Sessions() store.SessionStore { return s.sessions }

// Outbox returns the OutboxStore.
func (s *Store) Outbox() store.OutboxStore { return s.outbox }

// Definitions returns the DefinitionStore.
func (s *Store) Definitions() store.DefinitionStore { return s.defs }

// DefinitionEvents returns the DefinitionEventStore.
func (s *Store) DefinitionEvents() store.DefinitionEventStore { return s.defEvents }

// PreflightResults returns the PreflightStore.
func (s *Store) PreflightResults() store.PreflightStore { return s.preflight }

// Values returns the ValuesStore.
func (s *Store) Values() store.ValuesStore { return s.vals }

// ValuesApproval returns the atomic approval workflow store.
func (s *Store) ValuesApproval() store.ValuesApprovalStore { return s.valuesApproval }

// ValuesApprovalEvidence returns immutable workflow evidence readers.
func (s *Store) ValuesApprovalEvidence() store.ValuesApprovalReader { return s.valuesApproval }

// ValuesLifecycle returns the atomic create-and-discard store.
func (s *Store) ValuesLifecycle() store.ValuesLifecycleStore { return s.valuesLifecycle }

// PrepareSessions returns the short-lived convergence preparation store.
func (s *Store) PrepareSessions() store.PrepareSessionStore { return s.prepareSessions }

// Users returns the UserStore.
func (s *Store) Users() store.UserStore { return s.users }

// AuthSessions returns the AuthSessionStore.
func (s *Store) AuthSessions() store.AuthSessionStore { return s.authSess }

// Organizations returns the OrganizationStore.
func (s *Store) Organizations() store.OrganizationStore { return s.orgs }

// OrgMembers returns the OrganizationMemberStore.
func (s *Store) OrgMembers() store.OrganizationMemberStore { return s.orgMembers }

// Bindings returns the BindingStore.
func (s *Store) Bindings() store.BindingStore { return s.bindings }

// AuditEvents returns the AuditEventStore.
func (s *Store) AuditEvents() store.AuditEventStore { return s.audit }

// AuditExports returns the AuditExportStore.
func (s *Store) AuditExports() store.AuditExportStore { return s.auditExports }

// Bundles returns the BundleStore.
func (s *Store) Bundles() store.BundleStore { return s.bundles }

// Notifications returns the NotificationStore.
func (s *Store) Notifications() store.NotificationStore { return s.notif }

// Idempotency returns the IdempotencyStore.
func (s *Store) Idempotency() store.IdempotencyStore { return s.idem }

// CleanupIdempotency returns an explicit PostgreSQL-only implementation.
func (s *Store) CleanupIdempotency() store.CleanupIdempotencyStore {
	return unsupportedCleanupIdempotencyStore{}
}

// Verifications returns the VerificationStore.
func (s *Store) Verifications() store.VerificationStore { return s.verifs }

// TrustRoots returns the TrustRootStore.
func (s *Store) TrustRoots() store.TrustRootStore { return s.trustRoots }

// ScanResults returns the ScanResultStore.
func (s *Store) ScanResults() store.ScanResultStore { return s.scanResults }

// VulnerabilityExceptions returns the VulnerabilityExceptionStore.
func (s *Store) VulnerabilityExceptions() store.VulnerabilityExceptionStore { return s.vulnExceptions }

// CustomerEvents returns the CustomerEventStore.
func (s *Store) CustomerEvents() store.CustomerEventStore { return s.custEvents }

// ClusterRoutes returns the ClusterRouteStore.
func (s *Store) ClusterRoutes() store.ClusterRouteStore { return s.routes }

// Inventories returns the InventoryStore.
func (s *Store) Inventories() store.InventoryStore { return s.invs }

// InventorySyncRequests returns the persistent manual inventory sync request store.
func (s *Store) InventorySyncRequests() store.InventorySyncRequestStore { return s.syncRequests }

// CandidateArtifacts returns the CandidateArtifactStore.
func (s *Store) CandidateArtifacts() store.CandidateArtifactStore { return s.candidateArts }

func (s *Store) ArtifactEvents() store.ArtifactEventStore { return unsupportedArtifactEventStore{} }

func (s *Store) ValidationOutbox() store.ValidationOutboxStore {
	return unsupportedValidationOutboxStore{}
}

func (s *Store) BundleSubmissions() store.BundleSubmissionStore {
	return unsupportedBundleSubmissionStore{}
}

func (s *Store) ArtifactEventSubmissions() store.ArtifactEventSubmissionStore {
	return unsupportedArtifactEventSubmissionStore{}
}

// PreflightLifecycles returns the PreflightLifecycleStore.
func (s *Store) PreflightLifecycles() store.PreflightLifecycleStore { return s.preflightCycles }

// EmergencyIntents returns the EmergencyIntentStore.
func (s *Store) EmergencyIntents() store.EmergencyIntentStore { return s.emergencyIntents }

// ConvergenceTasks returns the ConvergenceTaskStore.
func (s *Store) ConvergenceTasks() store.ConvergenceTaskStore { return s.convergenceTasks }

// EmergencyConfig returns the emergency kill-switch/timeout configuration store.
func (s *Store) EmergencyConfig() store.EmergencyConfigStore { return s.emergencyConfig }

// Authorization returns the durable authorization state module.
func (s *Store) Authorization() store.AuthorizationStore { return s.authorization }

// Close closes the underlying database connection.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying *sql.DB for testing.
func (s *Store) DB() *sql.DB { return s.db }

var testDatabaseSequence atomic.Uint64

// OpenTest creates a Store backed by an in-memory SQLite database for testing.
// The caller is responsible for closing the store via t.Cleanup.
func OpenTest(t interface{ Cleanup(func()) }) *Store {
	dsn := fmt.Sprintf("file:release-manager-test-%d?mode=memory&cache=shared", testDatabaseSequence.Add(1))
	st, err := Open(dsn)
	if err != nil {
		panic("sqlite OpenTest: " + err.Error())
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// migrate runs the ordered migration steps against the database.
//
// Empty databases take a fast path: instead of replaying the incremental
// ALTER history — each ALTER TABLE round trip costs ~8ms under the race
// detector even on an empty table, and the test suites open hundreds of
// empty stores per run (the orchestrator suite alone opens 272 stores ≈
// 2.5min of pure migration) — they receive a page-level clone of a
// per-process template that was itself migrated by the legacy loop once. The
// clone is byte-identical to the legacy outcome by construction. When the
// backup API is unavailable the fast path degrades to replaying the
// canonical DDL snapshot read back from sqlite_master; both paths fall back
// to the legacy loop on any uncertainty. Databases carrying any schema
// objects always go through the legacy loop, keeping their
// repair/idempotency semantics.
func migrate(db *sql.DB, dsn string) error {
	fresh, err := databaseIsFresh(db)
	if err != nil {
		return fmt.Errorf("sqlite freshness check: %w", err)
	}
	if !fresh {
		return migrateLegacy(db)
	}
	if backupCapableDSN(dsn) {
		if err := cloneFreshSchema(db, dsn); err == nil {
			return nil
		} else if !errors.Is(err, errFreshBackupUnavailable) {
			return fmt.Errorf("sqlite fresh clone: %w", err)
		}
	}
	if ddl, seed, ok := freshSchema(); ok {
		return migrateFresh(db, ddl, seed)
	}
	return migrateLegacy(db)
}

// databaseIsFresh reports whether the database carries no schema objects yet.
// Only a completely empty database takes the fresh fast path: any user table,
// index, trigger or view (even a legacy-era schema that predates the
// release_definitions anchor table) must go through the legacy repair loop.
func databaseIsFresh(db *sql.DB) (bool, error) {
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`,
	).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// errFreshBackupUnavailable reports that the modernc driver connection does
// not expose the NewBackup method (driver internals changed).
var errFreshBackupUnavailable = errors.New("fresh-schema backup API unavailable")

// backupCapableDSN reports whether a second connection (the backup target
// opened by modernc's NewBackup) can address the same database as the DSN.
// Private (non-shared) in-memory databases are excluded: every connection
// there gets its own empty database, so a backup would silently write a
// different database and leave the caller's database empty.
func backupCapableDSN(dsn string) bool {
	if dsn == ":memory:" {
		return false
	}
	if strings.Contains(dsn, "mode=memory") && !strings.Contains(dsn, "cache=shared") {
		return false
	}
	return true
}

var (
	freshTemplateOnce sync.Once
	freshTemplateDB   *sql.DB
	freshTemplateErr  error
)

// freshTemplate returns a migrated in-memory database used as the page-level
// source for fresh-database schema clones. It is built once per process by
// running the authoritative legacy migration, so a clone is byte-identical
// to the legacy outcome. The template stays open for the process lifetime
// (a few pages in memory).
func freshTemplate() (*sql.DB, error) {
	freshTemplateOnce.Do(func() {
		db, err := sql.Open("sqlite", "file:release-manager-schema-template?mode=memory&cache=shared")
		if err != nil {
			freshTemplateErr = fmt.Errorf("open schema template: %w", err)
			return
		}
		if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
			db.Close()
			freshTemplateErr = fmt.Errorf("schema template journal_mode: %w", err)
			return
		}
		if _, err := db.ExecContext(context.Background(), "PRAGMA foreign_keys=ON"); err != nil {
			db.Close()
			freshTemplateErr = fmt.Errorf("schema template foreign_keys: %w", err)
			return
		}
		if err := migrateLegacy(db); err != nil {
			db.Close()
			freshTemplateErr = fmt.Errorf("migrate schema template: %w", err)
			return
		}
		freshTemplateDB = db
	})
	return freshTemplateDB, freshTemplateErr
}

// cloneFreshSchema backs the migrated template up into the fresh database the
// caller already has open. The backup destination reuses the original DSN,
// so the clone lands in the exact database `db` is connected to (a file path
// or a cache=shared memory URI).
func cloneFreshSchema(db *sql.DB, dsn string) error {
	tmpl, err := freshTemplate()
	if err != nil {
		return err
	}
	c, err := tmpl.Conn(context.Background())
	if err != nil {
		return err
	}
	defer c.Close()
	var newBackup func(string) (*modernc.Backup, error)
	if err := c.Raw(func(dc any) error {
		b, ok := dc.(interface {
			NewBackup(string) (*modernc.Backup, error)
		})
		if !ok {
			return errFreshBackupUnavailable
		}
		newBackup = b.NewBackup
		return nil
	}); err != nil {
		return err
	}
	backup, err := newBackup(dsn)
	if err != nil {
		return err
	}
	done, err := backup.Step(-1)
	if err != nil {
		_ = backup.Finish() //nolint:errcheck // best-effort cleanup on the error path; the Step error is authoritative
		return err
	}
	if done {
		_ = backup.Finish() //nolint:errcheck // best-effort cleanup; the anomaly error below is authoritative
		return fmt.Errorf("schema backup did not finish in one step")
	}
	if err := backup.Finish(); err != nil {
		return err
	}
	// The clone may have rewritten the file header (the in-memory template
	// ignores journal_mode); re-assert WAL on the caller's connection so
	// file-backed databases keep the WAL journal mode.
	if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
		return fmt.Errorf("re-assert journal_mode after clone: %w", err)
	}
	return nil
}

// migrateLegacy is the authoritative incremental migration: ordered DDL with
// Go-side branches for legacy values/preflight/enrollment-token shapes. It
// runs for every pre-existing database and doubles as the builder of the
// fresh-schema snapshot.
//
// ALTER TABLE ADD COLUMN statements that fail because the column already
// exists are silently skipped to keep migrations idempotent.
//
//nolint:gocyclo // serialized, ordered migration gate with one guard branch per schema step (project convention).
func migrateLegacy(db *sql.DB) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}
	valuesSchemaMigrated := false
	preflightSchemaMigrated := false
	enrollmentTokenMigrated := false
	for _, stmt := range migrationStatements {
		if !valuesSchemaMigrated && strings.Contains(stmt, "ux_vr_def_version") {
			if err := migrateValuesRevisionSchema(tx); err != nil {
				return fmt.Errorf("migrate values revision schema: %w", err)
			}
			valuesSchemaMigrated = true
		}
		if !preflightSchemaMigrated && strings.HasPrefix(strings.TrimSpace(stmt), "CREATE TABLE IF NOT EXISTS preflight_lifecycles") {
			if err := migratePreflightLifecycleSchema(tx); err != nil {
				return fmt.Errorf("migrate preflight lifecycle schema: %w", err)
			}
			preflightSchemaMigrated = true
		}
		if !enrollmentTokenMigrated && strings.HasPrefix(strings.TrimSpace(stmt), "CREATE TABLE IF NOT EXISTS enrollment_tokens") {
			if err := migrateEnrollmentTokenDropPlaintext(tx); err != nil {
				return fmt.Errorf("migrate enrollment token schema: %w", err)
			}
			enrollmentTokenMigrated = true
		}
		if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
			// ALTER TABLE ADD COLUMN is not idempotent; skip if the
			// column already exists (added by a prior migration run
			// or by a CREATE TABLE that already includes it).
			errStr := err.Error()
			if strings.Contains(stmt, "ALTER TABLE") &&
				strings.Contains(stmt, "ADD COLUMN") &&
				strings.Contains(errStr, "duplicate column") {
				continue
			}
			return fmt.Errorf("migration statement: %w\nstmt: %s", err, stmt)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

// freshSchemaState is the per-process canonical snapshot of a freshly
// migrated database.
type freshSchemaState struct {
	ddl  string // ordered end-state DDL read back from sqlite_master
	seed string // data-level statements to replay after the DDL
}

var (
	freshSchemaOnce sync.Once
	freshSchemaData freshSchemaState
	freshSchemaOK   bool
)

// freshSchema returns the canonical fresh-database schema, built on first use
// by running the legacy migration against a scratch database. ok is false
// when the migration contains statement shapes the snapshot cannot faithfully
// capture (see buildFreshSchema); callers must then use the legacy path.
func freshSchema() (ddl, seed string, ok bool) {
	freshSchemaOnce.Do(func() {
		freshSchemaData, freshSchemaOK = buildFreshSchema()
	})
	if !freshSchemaOK {
		return "", "", false
	}
	return freshSchemaData.ddl, freshSchemaData.seed, true
}

// buildFreshSchema replays the legacy migration on a scratch database and
// captures the resulting end-state: all schema-level statements are folded
// into the sqlite_master DDL snapshot (so no incremental ALTER history is
// replayed on fresh databases), while data-level statements (INSERT/DELETE)
// are preserved verbatim for replay after the DDL. UPDATEs are skipped — on
// an empty database they are no-ops.
//
// The function fails closed (ok=false) on any statement shape it cannot
// capture: unknown statement prefixes or virtual tables (whose shadow tables
// would be replayed twice).
func buildFreshSchema() (freshSchemaState, bool) {
	var seed strings.Builder
	for _, stmt := range migrationStatements {
		verb := strings.ToUpper(strings.TrimSpace(stmt))
		switch {
		case strings.HasPrefix(verb, "CREATE "),
			strings.HasPrefix(verb, "ALTER "),
			strings.HasPrefix(verb, "DROP "),
			strings.HasPrefix(verb, "UPDATE "):
			// Captured by the sqlite_master snapshot below.
		case strings.HasPrefix(verb, "INSERT "), strings.HasPrefix(verb, "DELETE "):
			seed.WriteString(stmt)
			seed.WriteString(";\n")
		default:
			return freshSchemaState{}, false
		}
	}

	db, err := sql.Open("sqlite", "file:release-manager-fresh-schema-snapshot?mode=memory&cache=shared")
	if err != nil {
		return freshSchemaState{}, false
	}
	defer db.Close()
	if err := migrateLegacy(db); err != nil {
		return freshSchemaState{}, false
	}

	var ddl strings.Builder
	// Tables first, then indexes/triggers/views; within each type, sqlite_master
	// rowid order is creation order, which already satisfies foreign-key
	// parent-before-child ordering (no table is ever dropped during a fresh
	// migration, so rowid order is stable).
	for _, typ := range []string{"table", "index", "trigger", "view"} {
		ok, err := appendTypeDDL(&ddl, db, typ)
		if err != nil || !ok {
			return freshSchemaState{}, false
		}
	}
	return freshSchemaState{ddl: ddl.String(), seed: seed.String()}, true
}

// appendTypeDDL appends the sqlite_master sql texts of the given object type
// to ddl in rowid (creation) order. It returns ok=false without error when the
// type contains a virtual table, whose shadow tables would be replayed twice
// by a snapshot.
func appendTypeDDL(ddl *strings.Builder, db *sql.DB, typ string) (ok bool, err error) {
	rows, err := db.QueryContext(context.Background(),
		`SELECT sql FROM sqlite_master
		 WHERE type = ? AND sql IS NOT NULL AND name NOT LIKE 'sqlite_%'
		 ORDER BY rowid`, typ)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var stmt string
		if err := rows.Scan(&stmt); err != nil {
			return false, err
		}
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(stmt)), "CREATE VIRTUAL TABLE") {
			return false, nil
		}
		ddl.WriteString(stmt)
		ddl.WriteString(";\n")
	}
	return true, rows.Err()
}

// migrateFresh applies the canonical snapshot to a fresh database in a single
// transaction. Unlike the legacy loop there are no expected statement
// failures, so any error aborts the migration loudly.
func migrateFresh(db *sql.DB, ddl, seed string) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}
	if _, err := tx.ExecContext(context.Background(), ddl); err != nil {
		_ = tx.Rollback() //nolint:errcheck // Rollback after a failed Exec is best-effort; the Exec error is authoritative
		return fmt.Errorf("apply fresh schema: %w", err)
	}
	if seed != "" {
		if _, err := tx.ExecContext(context.Background(), seed); err != nil {
			_ = tx.Rollback() //nolint:errcheck // Rollback after a failed Exec is best-effort; the Exec error is authoritative
			return fmt.Errorf("apply fresh seed: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

func migrateValuesRevisionSchema(tx *sql.Tx) error {
	columns, err := sqliteTableColumns(tx, "values_revisions")
	if err != nil {
		return err
	}
	if _, legacy := columns["revision"]; legacy {
		if _, hasStateVersion := columns["state_version"]; !hasStateVersion {
			return nil
		}
		if _, err := tx.ExecContext(context.Background(), `UPDATE values_revisions SET version = revision`); err != nil {
			return fmt.Errorf("backfill values version: %w", err)
		}
		if _, err := tx.ExecContext(context.Background(), `ALTER TABLE values_revisions DROP COLUMN revision`); err != nil {
			return fmt.Errorf("drop legacy values revision column: %w", err)
		}
	}

	if err := rebuildValuesRevisionsTable(tx); err != nil {
		return err
	}
	return rebuildValuesRevisionDecisionsTable(tx)
}

// migratePreflightLifecycleSchema rebuilds legacy preflight_lifecycles tables to
// the REQ-019 two-phase contract: operation_id UNIQUE, canonical comma-separated
// stages, four-value overall (timeout mapped to cancelled), and updated_at, with
// the error_code column removed. Legacy rows are deduplicated to one row per
// operation (keeping the earliest created_at), and updated_at is backfilled from
// created_at. Fresh databases (table not yet created) are left to the CREATE
// TABLE statement in migrationStatements.
func migratePreflightLifecycleSchema(tx *sql.Tx) error {
	columns, err := sqliteTableColumns(tx, "preflight_lifecycles")
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		return nil // table does not exist yet — the CREATE TABLE below defines the new shape
	}
	if _, hasUpdatedAt := columns["updated_at"]; hasUpdatedAt {
		if _, hasErrorCode := columns["error_code"]; !hasErrorCode {
			return nil // already on the two-phase contract
		}
	}

	statements := []string{
		`DROP INDEX IF EXISTS idx_preflight_lifecycles_operation`,
		`DROP INDEX IF EXISTS idx_preflight_lifecycles_terminal`,
		`ALTER TABLE preflight_lifecycles RENAME TO preflight_lifecycles_legacy`,
		`CREATE TABLE preflight_lifecycles (
			id                    TEXT PRIMARY KEY,
			operation_id          TEXT UNIQUE,
			operation_terminal_at TEXT,
			stages                TEXT NOT NULL DEFAULT '',
			overall               TEXT NOT NULL DEFAULT 'running',
			created_at            TEXT NOT NULL,
			updated_at            TEXT NOT NULL,
			CHECK (overall IN ('running','passed','failed','cancelled'))
		)`,
		`INSERT INTO preflight_lifecycles (id, operation_id, operation_terminal_at, stages, overall, created_at, updated_at)
		 SELECT
			l.id,
			l.operation_id,
			COALESCE(
				l.operation_terminal_at,
				(SELECT pl2.operation_terminal_at FROM preflight_lifecycles_legacy pl2
				 WHERE pl2.operation_id IS l.operation_id
				   AND pl2.operation_id IS NOT NULL
				   AND pl2.operation_terminal_at IS NOT NULL
				 ORDER BY pl2.created_at DESC LIMIT 1)
			),
			CASE
				WHEN l.stages IS NULL OR l.stages = '' OR l.stages = '[]' THEN ''
				WHEN json_valid(l.stages) AND json_type(l.stages) = 'array' THEN
					COALESCE((SELECT group_concat(json_extract(value, '$.stage'), ',') FROM json_each(l.stages)), '')
				ELSE ''
			END,
			CASE
				WHEN l.overall = 'timeout' THEN 'cancelled'
				WHEN l.overall IN ('running','passed','failed','cancelled') THEN l.overall
				ELSE 'failed'
			END,
			m.min_created,
			m.min_created
		 FROM (
			SELECT pl.*,
				CASE WHEN pl.operation_id IS NULL THEN pl.id ELSE pl.operation_id END AS group_key,
				ROW_NUMBER() OVER (
					PARTITION BY CASE WHEN pl.operation_id IS NULL THEN pl.id ELSE pl.operation_id END
					ORDER BY pl.created_at DESC
				) AS rn
			FROM preflight_lifecycles_legacy pl
		 ) l
		 JOIN (
			SELECT CASE WHEN operation_id IS NULL THEN id ELSE operation_id END AS group_key,
				MIN(created_at) AS min_created
			FROM preflight_lifecycles_legacy
			GROUP BY CASE WHEN operation_id IS NULL THEN id ELSE operation_id END
		 ) m ON m.group_key IS l.group_key
		 WHERE l.rn = 1`,
		`DROP TABLE preflight_lifecycles_legacy`,
		`CREATE INDEX IF NOT EXISTS idx_preflight_lifecycles_terminal ON preflight_lifecycles(operation_terminal_at)`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
			return fmt.Errorf("preflight lifecycle migration statement: %w\nstmt: %s", err, stmt)
		}
	}
	return nil
}

// migrateEnrollmentTokenDropPlaintext rebuilds enrollment_tokens without the
// legacy plaintext-capable `token` column (REQ-015 安全边界: 持久化仅存
// SHA-256 哈希). The current writer already stores only the hash in the
// legacy column; the rebuild keeps token_hash and drops the column entirely.
// COALESCE keeps legacy rows queryable regardless of which column held the
// hash. Fresh databases (table not yet created) are left to the CREATE TABLE
// below, which no longer defines the column.
func migrateEnrollmentTokenDropPlaintext(tx *sql.Tx) error {
	columns, err := sqliteTableColumns(tx, "enrollment_tokens")
	if err != nil {
		return err
	}
	if len(columns) == 0 {
		return nil // table does not exist yet — the CREATE TABLE below defines the new shape
	}
	if _, hasToken := columns["token"]; !hasToken {
		return nil // already on the hash-only shape
	}
	statements := []string{
		`ALTER TABLE enrollment_tokens RENAME TO enrollment_tokens_legacy`,
		`CREATE TABLE enrollment_tokens (
			id                      TEXT PRIMARY KEY,
			customer_id             TEXT NOT NULL,
			cluster_id              TEXT NOT NULL,
			token_hash              TEXT NOT NULL DEFAULT '',
			operator_name           TEXT NOT NULL DEFAULT '',
			state                   TEXT NOT NULL DEFAULT 'pending',
			created_by_display_name TEXT NOT NULL DEFAULT '',
			created_at              TEXT NOT NULL,
			expires_at              TEXT NOT NULL,
			used_at                 TEXT,
			operator_id             TEXT NOT NULL DEFAULT '',
			revoked_at              TEXT,
			replaced_by_id          TEXT NOT NULL DEFAULT ''
		)`,
		`INSERT INTO enrollment_tokens (
			id, customer_id, cluster_id, token_hash, operator_name, state,
			created_by_display_name, created_at, expires_at, used_at,
			operator_id, revoked_at, replaced_by_id
		) SELECT
			id, customer_id, cluster_id, COALESCE(NULLIF(token_hash, ''), token),
			operator_name, state, created_by_display_name, created_at, expires_at,
			used_at, operator_id, revoked_at, replaced_by_id
		FROM enrollment_tokens_legacy`,
		`DROP TABLE enrollment_tokens_legacy`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_enrollment_tokens_pending_cluster ON enrollment_tokens(cluster_id) WHERE state = 'pending'`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(context.Background(), stmt); err != nil {
			return fmt.Errorf("enrollment token migration statement: %w\nstmt: %s", err, stmt)
		}
	}
	return nil
}

func rebuildValuesRevisionsTable(tx *sql.Tx) error {
	var definition string
	if err := tx.QueryRowContext(context.Background(), `
		SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'values_revisions'
	`).Scan(&definition); err != nil {
		return fmt.Errorf("read values revision schema: %w", err)
	}
	if strings.Contains(definition, "'discarded'") && strings.Contains(definition, "ON DELETE RESTRICT") {
		return nil
	}

	statements := []string{
		`ALTER TABLE values_revisions RENAME TO values_revisions_legacy`,
		`CREATE TABLE values_revisions (
			id                    TEXT PRIMARY KEY,
			release_definition_id TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE RESTRICT,
			version               INTEGER NOT NULL DEFAULT 1,
			status                TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','pending_approval','approved','rejected','superseded','discarded')),
			"values"              BLOB NOT NULL,
			digest                TEXT NOT NULL DEFAULT '',
			parent_revision_id    TEXT REFERENCES values_revisions(id) ON DELETE RESTRICT,
			secret_refs           BLOB,
			created_by            TEXT NOT NULL DEFAULT '',
			approved_by           TEXT NOT NULL DEFAULT '',
			approved_at           TEXT,
			rejected_by           TEXT NOT NULL DEFAULT '',
			rejection_reason      TEXT NOT NULL DEFAULT '',
			state_version         INTEGER NOT NULL DEFAULT 0,
			created_by_user_id    TEXT NOT NULL DEFAULT '',
			submitted_at          TEXT,
			decided_at            TEXT,
			convergence_task_ids  TEXT NOT NULL DEFAULT '[]',
			locked_paths          TEXT NOT NULL DEFAULT '[]',
			created_at            TEXT NOT NULL,
			updated_at            TEXT NOT NULL,
			CHECK (parent_revision_id IS NOT NULL OR version = 1)
		)`,
		`INSERT INTO values_revisions (
			id, release_definition_id, version, status, "values", digest,
			parent_revision_id, secret_refs, created_by, approved_by, approved_at,
			rejected_by, rejection_reason, state_version, created_by_user_id,
			submitted_at, decided_at, convergence_task_ids, locked_paths, created_at, updated_at
		) SELECT id, release_definition_id, version, status, "values", digest,
			NULLIF(parent_revision_id, ''), secret_refs, created_by, approved_by, approved_at,
			rejected_by, rejection_reason, state_version, created_by_user_id,
			submitted_at, decided_at, '[]', '[]', created_at, updated_at FROM values_revisions_legacy`,
		`DROP TABLE values_revisions_legacy`,
		`CREATE INDEX IF NOT EXISTS idx_values_def ON values_revisions(release_definition_id)`,
		`CREATE INDEX IF NOT EXISTS idx_values_digest ON values_revisions(release_definition_id, digest)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ux_vr_one_approved_per_def
		 ON values_revisions(release_definition_id) WHERE status = 'approved'`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ux_vr_one_pending_per_def
		 ON values_revisions(release_definition_id) WHERE status = 'pending_approval'`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(context.Background(), statement); err != nil {
			return fmt.Errorf("rebuild values revisions: %w", err)
		}
	}
	return nil
}

func rebuildValuesRevisionDecisionsTable(tx *sql.Tx) error {
	var definition string
	err := tx.QueryRowContext(context.Background(), `
		SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'values_revision_decisions'
	`).Scan(&definition)
	if errors.Is(err, sql.ErrNoRows) || strings.Contains(definition, "'discarded'") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read values decision schema: %w", err)
	}
	statements := []string{
		`ALTER TABLE values_revision_decisions RENAME TO values_revision_decisions_legacy`,
		`CREATE TABLE values_revision_decisions (
			id                    TEXT PRIMARY KEY,
			revision_id           TEXT NOT NULL REFERENCES values_revisions(id) ON DELETE RESTRICT,
			release_definition_id TEXT NOT NULL,
			action                TEXT NOT NULL CHECK (action IN ('submitted', 'approved', 'rejected', 'discarded')),
			from_state            TEXT NOT NULL,
			to_state              TEXT NOT NULL,
			actor_user_id         TEXT NOT NULL,
			actor_org_id          TEXT NOT NULL,
			actor_role            TEXT NOT NULL DEFAULT '',
			comment               TEXT,
			reason                TEXT NOT NULL DEFAULT '',
			request_id            TEXT NOT NULL DEFAULT '',
			idempotency_key_hash  TEXT NOT NULL DEFAULT '',
			created_at            TEXT NOT NULL
		)`,
		`INSERT INTO values_revision_decisions (
			id, revision_id, release_definition_id, action, from_state, to_state,
			actor_user_id, actor_org_id, actor_role, comment, reason, request_id,
			idempotency_key_hash, created_at
		) SELECT id, revision_id, release_definition_id, action, from_state, to_state,
			actor_user_id, actor_org_id, actor_role, comment, reason, request_id,
			idempotency_key_hash, created_at FROM values_revision_decisions_legacy`,
		`DROP TABLE values_revision_decisions_legacy`,
		`CREATE INDEX IF NOT EXISTS idx_values_revision_decisions_revision ON values_revision_decisions(revision_id, created_at)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(context.Background(), statement); err != nil {
			return fmt.Errorf("rebuild values revision decisions: %w", err)
		}
	}
	return nil
}

func sqliteTableColumns(tx *sql.Tx, table string) (map[string]struct{}, error) {
	rows, err := tx.QueryContext(context.Background(), `PRAGMA table_info(`+table+`)`) //nolint:gosec // table is a fixed internal identifier.
	if err != nil {
		return nil, fmt.Errorf("read %s columns: %w", table, err)
	}
	defer rows.Close()
	columns := make(map[string]struct{})
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&position, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("scan %s columns: %w", table, err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s columns: %w", table, err)
	}
	return columns, nil
}

// migrationStatements contains the ordered DDL for the core pipeline schema.
// Using IF NOT EXISTS so re-runs are idempotent.
var migrationStatements = []string{
	`CREATE TABLE IF NOT EXISTS release_definitions (
		id                  TEXT PRIMARY KEY,
		name                TEXT NOT NULL,
		customer_id         TEXT NOT NULL,
		cluster_id          TEXT NOT NULL,
		namespace           TEXT NOT NULL DEFAULT '',
		release_name        TEXT NOT NULL,
		chart_name          TEXT NOT NULL DEFAULT '',
		status              TEXT NOT NULL DEFAULT 'draft',
		optimistic_version  INTEGER NOT NULL DEFAULT 0,
		created_by          TEXT NOT NULL DEFAULT '',
		created_at          TEXT NOT NULL,
		updated_at          TEXT NOT NULL,
		UNIQUE(customer_id, cluster_id, namespace, release_name)
	)`,

	`CREATE TABLE IF NOT EXISTS release_definition_events (
		id            TEXT PRIMARY KEY,
		definition_id TEXT NOT NULL REFERENCES release_definitions(id),
		event_type    TEXT NOT NULL,
		created_at    TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_release_definition_events_definition ON release_definition_events(definition_id, created_at)`,

	`CREATE TABLE IF NOT EXISTS values_revisions (
		id                    TEXT PRIMARY KEY,
		release_definition_id TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE RESTRICT,
		version               INTEGER NOT NULL DEFAULT 1,
		status                TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','pending_approval','approved','rejected','superseded','discarded')),
		"values"              BLOB NOT NULL,
		digest                TEXT NOT NULL DEFAULT '',
		parent_revision_id    TEXT REFERENCES values_revisions(id) ON DELETE RESTRICT,
		secret_refs           BLOB,
		created_by            TEXT NOT NULL DEFAULT '',
		approved_by           TEXT NOT NULL DEFAULT '',
		approved_at           TEXT,
		rejected_by           TEXT NOT NULL DEFAULT '',
		rejection_reason      TEXT NOT NULL DEFAULT '',
		created_at            TEXT NOT NULL,
		updated_at            TEXT NOT NULL,
		CHECK (parent_revision_id IS NOT NULL OR version = 1)
	)`,

	// Application-wide key/value settings (REQ-079 kill switch + timeout).
	`CREATE TABLE IF NOT EXISTS app_settings (
		key        TEXT PRIMARY KEY,
		value      TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,

	// Migration: add approval workflow columns to existing values revisions.
	`ALTER TABLE values_revisions ADD COLUMN digest TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE values_revisions ADD COLUMN parent_revision_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE values_revisions ADD COLUMN secret_refs BLOB`,
	`ALTER TABLE values_revisions ADD COLUMN version INTEGER NOT NULL DEFAULT 1`,
	`ALTER TABLE values_revisions ADD COLUMN created_by TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE values_revisions ADD COLUMN approved_by TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE values_revisions ADD COLUMN approved_at TEXT`,
	`ALTER TABLE values_revisions ADD COLUMN rejected_by TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE values_revisions ADD COLUMN rejection_reason TEXT NOT NULL DEFAULT ''`,
	// Values approval workflow (REQ-068).
	`ALTER TABLE values_revisions ADD COLUMN state_version INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE values_revisions ADD COLUMN created_by_user_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE values_revisions ADD COLUMN submitted_at TEXT`,
	`ALTER TABLE values_revisions ADD COLUMN decided_at TEXT`,
	// Convergence bindings (REQ-079 D10/D15): SQLite has no native arrays,
	// so the two fields use the JSON-encoded TEXT form (same convention as
	// secret_refs); Postgres stores text[]/uuid[] array columns.
	`ALTER TABLE values_revisions ADD COLUMN convergence_task_ids TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE values_revisions ADD COLUMN locked_paths TEXT NOT NULL DEFAULT '[]'`,
	`UPDATE values_revisions SET state_version = CASE WHEN version > 0 THEN version ELSE 1 END WHERE state_version = 0`,
	`UPDATE values_revisions SET created_by_user_id = created_by WHERE created_by_user_id = ''`,
	`ALTER TABLE release_definitions ADD COLUMN owner_organization_id TEXT`,
	`ALTER TABLE release_definitions ADD COLUMN approved_revision_id TEXT`,
	`ALTER TABLE release_definitions ADD COLUMN hpa_managed INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE release_definitions ADD COLUMN max_emergency_replicas INTEGER NOT NULL DEFAULT 100`,
	`ALTER TABLE release_definitions ADD COLUMN approved_annotation_keys BLOB NOT NULL DEFAULT '[]'`,
	`ALTER TABLE release_definitions ADD COLUMN promotion_mappings BLOB NOT NULL DEFAULT '[]'`,

	`CREATE TABLE IF NOT EXISTS values_revision_decisions (
		id                    TEXT PRIMARY KEY,
		revision_id           TEXT NOT NULL REFERENCES values_revisions(id) ON DELETE RESTRICT,
		release_definition_id TEXT NOT NULL,
		action                TEXT NOT NULL CHECK (action IN ('submitted', 'approved', 'rejected', 'discarded')),
		from_state            TEXT NOT NULL,
		to_state              TEXT NOT NULL,
		actor_user_id         TEXT NOT NULL,
		actor_org_id          TEXT NOT NULL,
		actor_role            TEXT NOT NULL DEFAULT '',
		comment               TEXT,
		reason                TEXT NOT NULL DEFAULT '',
		request_id            TEXT NOT NULL DEFAULT '',
		idempotency_key_hash  TEXT NOT NULL DEFAULT '',
		created_at            TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_values_revision_decisions_revision ON values_revision_decisions(revision_id, created_at)`,

	`CREATE TABLE IF NOT EXISTS idempotency_records (
		scope         TEXT NOT NULL,
		text_key      TEXT NOT NULL,
		request_hash  TEXT NOT NULL,
		response_ref  BLOB NOT NULL,
		expires_at    TEXT NOT NULL,
		PRIMARY KEY(scope, text_key)
	)`,

	`CREATE TABLE IF NOT EXISTS audit_outbox (
		id            TEXT PRIMARY KEY,
		event_type    TEXT NOT NULL,
		payload_json  BLOB NOT NULL,
		created_at    TEXT NOT NULL,
		delivered     INTEGER NOT NULL DEFAULT 0,
		delivered_at  TEXT
	)`,
	`CREATE TABLE IF NOT EXISTS notification_outbox (
		id            TEXT PRIMARY KEY,
		event_type    TEXT NOT NULL,
		payload_json  BLOB NOT NULL,
		created_at    TEXT NOT NULL,
		delivered     INTEGER NOT NULL DEFAULT 0,
		delivered_at  TEXT
	)`,

	// Normalize legacy duplicate approved rows before installing the invariant.
	`UPDATE values_revisions AS current
	 SET status = 'superseded', state_version = state_version + 1, updated_at = CURRENT_TIMESTAMP
	 WHERE status = 'approved'
	   AND EXISTS (
		SELECT 1 FROM values_revisions AS newer
		WHERE newer.release_definition_id = current.release_definition_id
		  AND newer.status = 'approved'
		  AND (newer.version > current.version OR (newer.version = current.version AND newer.id > current.id))
	 )`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_vr_one_approved_per_def
	 ON values_revisions(release_definition_id) WHERE status = 'approved'`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_vr_one_pending_per_def
	 ON values_revisions(release_definition_id) WHERE status = 'pending_approval'`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_vr_def_version
	 ON values_revisions(release_definition_id, version)`,

	`CREATE TABLE IF NOT EXISTS operations (
		id                   TEXT PRIMARY KEY,
		operation_type       TEXT NOT NULL,
		status               TEXT NOT NULL DEFAULT 'pending',
		release_definition_id TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE CASCADE,
		idempotency_key      TEXT NOT NULL UNIQUE,
		request_hash         TEXT NOT NULL,
		state_version        INTEGER NOT NULL DEFAULT 0,
		bundle_id            TEXT NOT NULL DEFAULT '',
		values_revision_id   TEXT NOT NULL DEFAULT '',
		expected_revision    INTEGER NOT NULL DEFAULT 0,
		values_patch         BLOB,
		actor                TEXT NOT NULL DEFAULT '{}',
		created_at           TEXT NOT NULL,
		updated_at           TEXT NOT NULL,
		deadline             TEXT,
		last_error           TEXT NOT NULL DEFAULT ''
	)`,
	// Migration: add ROLLBACK and summary columns (superset of v3 branch contract).
	`ALTER TABLE operations ADD COLUMN target_revision INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE operations ADD COLUMN target_operation_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE operations ADD COLUMN terminal_at TEXT`,
	`ALTER TABLE operations ADD COLUMN idempotency_scope TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE operations ADD COLUMN bundle_chart_ref TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE operations ADD COLUMN bundle_chart_digest TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE operations ADD COLUMN image_refs_json BLOB NOT NULL DEFAULT '[]'`,
	`ALTER TABLE operations ADD COLUMN image_digests_json BLOB NOT NULL DEFAULT '[]'`,
	`ALTER TABLE operations ADD COLUMN policy_version TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE operations ADD COLUMN patch_digest TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE operations ADD COLUMN effective_values_digest TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE operations ADD COLUMN reason TEXT NOT NULL DEFAULT ''`,
	`UPDATE operations
	 SET terminal_at = updated_at
	 WHERE status IN ('succeeded', 'failed', 'cancelled', 'timeout') AND terminal_at IS NULL`,

	`CREATE INDEX IF NOT EXISTS idx_operations_definition ON operations(release_definition_id, status)`,
	`CREATE INDEX IF NOT EXISTS idx_operations_idempotency ON operations(idempotency_key)`,
	`CREATE INDEX IF NOT EXISTS idx_values_def ON values_revisions(release_definition_id)`,
	`CREATE INDEX IF NOT EXISTS idx_values_digest ON values_revisions(release_definition_id, digest)`,

	// Tenancy — customers and clusters (REQ-013, REQ-014)
	`CREATE TABLE IF NOT EXISTS customers (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		slug       TEXT NOT NULL UNIQUE,
		status     TEXT NOT NULL DEFAULT 'active',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,

	`ALTER TABLE customers ADD COLUMN version INTEGER NOT NULL DEFAULT 1`,

	`CREATE TABLE IF NOT EXISTS clusters (
		id             TEXT PRIMARY KEY,
		name           TEXT NOT NULL,
		customer_id    TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
		kubeconfig_ref TEXT NOT NULL DEFAULT '',
		status         TEXT NOT NULL DEFAULT 'active',
		created_at     TEXT NOT NULL,
		updated_at     TEXT NOT NULL
	)`,

	`ALTER TABLE clusters ADD COLUMN version INTEGER NOT NULL DEFAULT 1`,

	`CREATE INDEX IF NOT EXISTS idx_clusters_customer ON clusters(customer_id)`,

	// Operator enrollment and session management (REQ-015, REQ-044)
	`CREATE TABLE IF NOT EXISTS enrollment_tokens (
		id                    TEXT PRIMARY KEY,
		customer_id           TEXT NOT NULL,
		cluster_id            TEXT NOT NULL,
		token_hash            TEXT NOT NULL DEFAULT '',
		operator_name         TEXT NOT NULL DEFAULT '',
		state                 TEXT NOT NULL DEFAULT 'pending',
		created_by_display_name TEXT NOT NULL DEFAULT '',
		created_at            TEXT NOT NULL,
		expires_at            TEXT NOT NULL,
		used_at               TEXT,
		operator_id           TEXT NOT NULL DEFAULT '',
		revoked_at            TEXT,
		replaced_by_id        TEXT NOT NULL DEFAULT ''
	)`,

	// REQ-015: token_hash column for enrollment token security
	`ALTER TABLE enrollment_tokens ADD COLUMN token_hash TEXT NOT NULL DEFAULT ''`,

	`CREATE TABLE IF NOT EXISTS operators (
		id            TEXT PRIMARY KEY,
		customer_id   TEXT NOT NULL,
		cluster_id    TEXT NOT NULL,
		operator_name TEXT NOT NULL DEFAULT '',
		cert_serial   TEXT NOT NULL,
		certificate_expires_at TEXT,
		status        TEXT NOT NULL DEFAULT 'active',
		superseded_by TEXT NOT NULL DEFAULT '',
		superseded_at TEXT,
		revoked_at    TEXT,
		revoke_reason TEXT NOT NULL DEFAULT '',
		created_at    TEXT NOT NULL,
		updated_at    TEXT NOT NULL
	)`,

	// ADR-018: cert serial is the identity authority — a unique index makes
	// an 80-bit DER-hash collision fail the insert instead of silently
	// binding two operators to one certificate.
	`CREATE UNIQUE INDEX IF NOT EXISTS operators_cert_serial_uq ON operators(cert_serial)`,
	`CREATE INDEX IF NOT EXISTS idx_operators_name ON operators(operator_name)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_operators_customer_active_name ON operators(customer_id, operator_name) WHERE status = 'active'`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_operators_cluster_active ON operators(cluster_id) WHERE status = 'active'`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_enrollment_tokens_pending_cluster ON enrollment_tokens(cluster_id) WHERE state = 'pending'`,
	`CREATE INDEX IF NOT EXISTS idx_operators_cluster ON operators(cluster_id, status)`,

	// REQ-015: operator_name column for existing databases
	`ALTER TABLE operators ADD COLUMN operator_name TEXT NOT NULL DEFAULT ''`,

	`CREATE TABLE IF NOT EXISTS sessions (
		id             TEXT PRIMARY KEY,
		operator_id    TEXT NOT NULL REFERENCES operators(id) ON DELETE CASCADE,
		customer_id    TEXT NOT NULL DEFAULT '',
		cluster_id     TEXT NOT NULL DEFAULT '',
		status         TEXT NOT NULL DEFAULT 'online',
		status_reason  TEXT,
		started_at     TEXT NOT NULL,
		last_heartbeat TEXT NOT NULL,
		expires_at     TEXT NOT NULL,
		closed_at      TEXT
	)`,

	`ALTER TABLE sessions ADD COLUMN instance_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE sessions ADD COLUMN version TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE sessions ADD COLUMN capabilities TEXT NOT NULL DEFAULT '{}'`,
	`ALTER TABLE sessions ADD COLUMN active_config_version TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_instance ON sessions(operator_id, instance_id)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_one_active_operator ON sessions(operator_id) WHERE status IN ('online', 'suspect')`,

	`CREATE INDEX IF NOT EXISTS idx_sessions_operator ON sessions(operator_id, status)`,

	// New columns: enrollment token state and metadata (REQ-NNN).
	`ALTER TABLE enrollment_tokens ADD COLUMN operator_name TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE enrollment_tokens ADD COLUMN state TEXT NOT NULL DEFAULT 'pending'`,
	`ALTER TABLE enrollment_tokens ADD COLUMN created_by_display_name TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE enrollment_tokens ADD COLUMN revoked_at TEXT`,
	`ALTER TABLE enrollment_tokens ADD COLUMN replaced_by_id TEXT NOT NULL DEFAULT ''`,
	// New columns: operator supersede and revoke reason (REQ-NNN).
	`ALTER TABLE operators ADD COLUMN superseded_at TEXT`,
	`ALTER TABLE operators ADD COLUMN revoke_reason TEXT NOT NULL DEFAULT ''`,
	// REQ-015: renew window authority (certificate_expires_at).
	`ALTER TABLE operators ADD COLUMN certificate_expires_at TEXT`,
	// New columns: session customer linkage and lifecycle (REQ-NNN).
	`ALTER TABLE sessions ADD COLUMN customer_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE sessions ADD COLUMN cluster_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE sessions ADD COLUMN status_reason TEXT`,
	`ALTER TABLE sessions ADD COLUMN closed_at TEXT`,

	// Command outbox (REQ-016)
	`CREATE TABLE IF NOT EXISTS outbox (
		id             TEXT PRIMARY KEY,
		command_id     TEXT NOT NULL DEFAULT '',
		operation_id   TEXT NOT NULL DEFAULT '',
		operation_type TEXT NOT NULL DEFAULT '',
		operator_id    TEXT NOT NULL,
		payload        BLOB NOT NULL DEFAULT (x''),
		status         TEXT NOT NULL DEFAULT 'pending',
		max_inflight   INTEGER NOT NULL DEFAULT 1,
		sequence       INTEGER NOT NULL DEFAULT 0,
		result_json    TEXT NOT NULL DEFAULT '',
		created_at     TEXT NOT NULL,
		updated_at     TEXT NOT NULL,
		delivered_at   TEXT,
		acked_at       TEXT
	)`,

	`CREATE INDEX IF NOT EXISTS idx_outbox_operator_status ON outbox(operator_id, status)`,
	`CREATE INDEX IF NOT EXISTS idx_outbox_sequence ON outbox(sequence)`,
	`CREATE INDEX IF NOT EXISTS idx_outbox_command_id ON outbox(command_id)`,

	// Migration: add new columns to existing outbox tables.
	`ALTER TABLE outbox ADD COLUMN command_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE outbox ADD COLUMN sequence INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE outbox ADD COLUMN operation_type TEXT NOT NULL DEFAULT ''`,

	`CREATE TABLE IF NOT EXISTS operation_execution_results (
		operation_id   TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE CASCADE,
		result_type    TEXT NOT NULL,
		result_payload BLOB NOT NULL,
		created_at     TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS rollout_trackings (
		operation_id   TEXT PRIMARY KEY REFERENCES operations(id) ON DELETE CASCADE,
		status         TEXT NOT NULL DEFAULT 'pending',
		resource_count INTEGER NOT NULL DEFAULT 0,
		ready_count    INTEGER NOT NULL DEFAULT 0,
		failed_count   INTEGER NOT NULL DEFAULT 0,
		last_error     TEXT NOT NULL DEFAULT '',
		created_at     TEXT NOT NULL,
		updated_at     TEXT NOT NULL
	)`,

	// Auth & RBAC — users, sessions, organizations, bindings (REQ-025, REQ-026, REQ-049)
	`CREATE TABLE IF NOT EXISTS users (
		id            TEXT PRIMARY KEY,
		username      TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		status        TEXT NOT NULL DEFAULT 'active',
		created_at    TEXT NOT NULL,
		updated_at    TEXT NOT NULL
	)`,
	`ALTER TABLE users ADD COLUMN provider TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE users ADD COLUMN subject TEXT NOT NULL DEFAULT ''`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_users_provider_subject ON users(provider, subject) WHERE provider != '' AND subject != ''`,

	`CREATE TABLE IF NOT EXISTS auth_sessions (
		id                 TEXT PRIMARY KEY,
		user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		token_family       TEXT NOT NULL,
		refresh_token_hash TEXT NOT NULL,
		expires_at         TEXT NOT NULL,
		created_at         TEXT NOT NULL,
		revoked            INTEGER NOT NULL DEFAULT 0
	)`,

	`CREATE INDEX IF NOT EXISTS idx_auth_sessions_family ON auth_sessions(token_family)`,
	`CREATE INDEX IF NOT EXISTS idx_auth_sessions_user ON auth_sessions(user_id)`,

	`CREATE TABLE IF NOT EXISTS organizations (
		id                 TEXT PRIMARY KEY,
		name               TEXT NOT NULL,
		status             TEXT NOT NULL DEFAULT 'active',
		optimistic_version INTEGER NOT NULL DEFAULT 0,
		created_at         TEXT NOT NULL,
		updated_at         TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS organization_members (
		org_id             TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
		user_id            TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		role               TEXT NOT NULL DEFAULT 'viewer',
		optimistic_version INTEGER NOT NULL DEFAULT 0,
		created_at         TEXT NOT NULL,
		updated_at         TEXT NOT NULL,
		PRIMARY KEY (org_id, user_id)
	)`,

	`CREATE TABLE IF NOT EXISTS org_customer_bindings (
		id                 TEXT PRIMARY KEY,
		org_id             TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
		customer_id        TEXT NOT NULL,
		status             TEXT NOT NULL DEFAULT 'active',
		optimistic_version INTEGER NOT NULL DEFAULT 0,
		created_at         TEXT NOT NULL,
		updated_at         TEXT NOT NULL,
		UNIQUE(org_id, customer_id)
	)`,

	`CREATE INDEX IF NOT EXISTS idx_bindings_org ON org_customer_bindings(org_id)`,

	`CREATE TABLE IF NOT EXISTS organization_customer_binding_events (
		id                 TEXT PRIMARY KEY,
		binding_id         TEXT NOT NULL REFERENCES org_customer_bindings(id) ON DELETE CASCADE,
		org_id             TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
		customer_id        TEXT NOT NULL,
		status             TEXT NOT NULL,
		optimistic_version INTEGER NOT NULL,
		changed_at         TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_binding_events_binding ON organization_customer_binding_events(binding_id, changed_at)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_binding_events_version ON organization_customer_binding_events(binding_id, optimistic_version)`,

	// Audit events (REQ-050)
	`CREATE TABLE IF NOT EXISTS audit_events (
		id               TEXT PRIMARY KEY,
		actor_kind       TEXT NOT NULL DEFAULT 'system',
		actor_id         TEXT NOT NULL DEFAULT '',
		organization_id  TEXT NOT NULL DEFAULT '',
		role             TEXT NOT NULL DEFAULT '',
		resource_type    TEXT NOT NULL DEFAULT '',
		resource_id      TEXT NOT NULL DEFAULT '',
		action           TEXT NOT NULL DEFAULT '',
		status           TEXT NOT NULL DEFAULT '',
		duration_ms      INTEGER NOT NULL DEFAULT 0,
		change_summary   TEXT NOT NULL DEFAULT '',
		metadata         TEXT NOT NULL DEFAULT '{}',
		created_at       TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_events_actor ON audit_events(actor_kind, actor_id)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_events_resource ON audit_events(resource_type, resource_id)`,
	`CREATE TABLE IF NOT EXISTS audit_exports (
	id              TEXT PRIMARY KEY,
	organization_id TEXT NOT NULL DEFAULT '',
	since           TEXT NOT NULL,
	until           TEXT NOT NULL,
	status          TEXT NOT NULL DEFAULT 'pending',
	created_at      TEXT NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_exports_organization ON audit_exports(organization_id, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_events_created ON audit_events(created_at)`,

	// Notification jobs (REQ-031)
	`CREATE TABLE IF NOT EXISTS notification_jobs (
		id             TEXT PRIMARY KEY,
		operation_id   TEXT NOT NULL DEFAULT '',
		channel        TEXT NOT NULL DEFAULT 'webhook',
		recipient      TEXT NOT NULL DEFAULT '',
		status         TEXT NOT NULL DEFAULT 'pending',
		attempts       INTEGER NOT NULL DEFAULT 0,
		retry_count    INTEGER NOT NULL DEFAULT 0,
		max_retries    INTEGER NOT NULL DEFAULT 3,
		error_code     TEXT NOT NULL DEFAULT '',
		next_retry_at  TEXT,
		last_error     TEXT NOT NULL DEFAULT '',
		sent_at        TEXT,
		dead_letter_at TEXT,
		metadata       TEXT NOT NULL DEFAULT '{}',
		created_at     TEXT NOT NULL,
		updated_at     TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_notification_jobs_status ON notification_jobs(status)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_notification_jobs_dedup ON notification_jobs(operation_id, channel, recipient)`,

	// REQ-031: Add columns that may be missing from existing DBs (idempotent ALTER TABLE).
	`ALTER TABLE notification_jobs ADD COLUMN next_retry_at TEXT`,
	`ALTER TABLE notification_jobs ADD COLUMN dead_letter_at TEXT`,
	`ALTER TABLE notification_jobs ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE notification_jobs ADD COLUMN error_code TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE notification_jobs ADD COLUMN sent_at TEXT`,
	// Artifact trust verification (REQ-012)
	`CREATE TABLE IF NOT EXISTS verification_records (
		id              TEXT PRIMARY KEY,
		artifact_digest TEXT NOT NULL,
		policy_version  TEXT NOT NULL DEFAULT '',
		status          TEXT NOT NULL DEFAULT '',
		issuer          TEXT NOT NULL DEFAULT '',
		subject         TEXT NOT NULL DEFAULT '',
		summary         TEXT NOT NULL DEFAULT '',
		created_at      TEXT NOT NULL
	)`,
	`ALTER TABLE verification_records ADD COLUMN root_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE verification_records ADD COLUMN key_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE verification_records ADD COLUMN revocation_epoch INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE verification_records ADD COLUMN signature_identity TEXT NOT NULL DEFAULT ''`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_verification_records_digest_policy ON verification_records(artifact_digest, policy_version, created_at)`,

	// Customer domain events (REQ-013)
	`CREATE TABLE IF NOT EXISTS customer_events (
		id          TEXT PRIMARY KEY,
		customer_id TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
		event_type  TEXT NOT NULL,
		created_at  TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_customer_events_customer ON customer_events(customer_id, event_type)`,

	// Operation state change events (REQ-023)
	`CREATE TABLE IF NOT EXISTS operation_events (
		id                    TEXT PRIMARY KEY,
		operation_id          TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
		operation_type        TEXT NOT NULL,
		release_definition_id TEXT NOT NULL,
		old_status            TEXT NOT NULL,
		new_status            TEXT NOT NULL,
		state_version         INTEGER NOT NULL,
		created_at            TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_operation_events_operation ON operation_events(operation_id)`,
	`CREATE TABLE IF NOT EXISTS operation_timeline (
		id            TEXT PRIMARY KEY,
		operation_id  TEXT NOT NULL REFERENCES operations(id) ON DELETE CASCADE,
		sequence      INTEGER NOT NULL,
		entry_type    TEXT NOT NULL,
		state_version INTEGER NOT NULL,
		data          BLOB NOT NULL DEFAULT '{}',
		created_at    TEXT NOT NULL,
		UNIQUE(operation_id, sequence)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_operation_timeline_operation ON operation_timeline(operation_id, sequence)`,

	// Cluster artifact routing (REQ-014)
	`CREATE TABLE IF NOT EXISTS cluster_routes (
		id            TEXT PRIMARY KEY,
		cluster_id    TEXT NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
		artifact_type TEXT NOT NULL,
		mode          TEXT NOT NULL,
		source_prefix TEXT NOT NULL DEFAULT '',
		target_prefix TEXT NOT NULL DEFAULT '',
		created_at    TEXT NOT NULL,
		updated_at    TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_cluster_routes_cluster ON cluster_routes(cluster_id, artifact_type)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_cluster_routes_unique ON cluster_routes(cluster_id, artifact_type, source_prefix)`,

	// Release inventory sync (REQ-017)
	`CREATE TABLE IF NOT EXISTS release_inventory (
		customer_id      TEXT NOT NULL,
		cluster_id       TEXT NOT NULL,
		release_definition_id TEXT NOT NULL DEFAULT '',
		namespace        TEXT NOT NULL DEFAULT '',
		release_name     TEXT NOT NULL,
		chart            TEXT NOT NULL DEFAULT '',
		chart_version    TEXT NOT NULL DEFAULT '',
		revision         INTEGER NOT NULL DEFAULT 0,
		status           TEXT NOT NULL DEFAULT '',
		values_digest    TEXT NOT NULL DEFAULT '',
		inventory_status TEXT NOT NULL DEFAULT 'active',
		last_sync_id     TEXT NOT NULL DEFAULT '',
		snapshot_version INTEGER NOT NULL DEFAULT 0,
		created_at       TEXT NOT NULL,
		updated_at       TEXT NOT NULL,
		UNIQUE(customer_id, cluster_id, namespace, release_name)
	)`,
	`ALTER TABLE release_inventory ADD COLUMN observed_bundle_digest TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE release_inventory ADD COLUMN observed_chart_digest TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE release_inventory ADD COLUMN observed_effective_values_digest TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE release_inventory ADD COLUMN observed_manifest_digest TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE release_inventory ADD COLUMN live_status TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE release_inventory ADD COLUMN last_operation_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE release_inventory ADD COLUMN release_definition_id TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS idx_inventory_cluster ON release_inventory(customer_id, cluster_id)`,
	`CREATE INDEX IF NOT EXISTS idx_inventory_status ON release_inventory(inventory_status)`,

	`CREATE TABLE IF NOT EXISTS inventory_sync_log (
		sync_id          TEXT PRIMARY KEY,
		customer_id      TEXT NOT NULL,
		cluster_id       TEXT NOT NULL,
		is_full_snapshot INTEGER NOT NULL DEFAULT 0,
		accepted_count   INTEGER NOT NULL DEFAULT 0,
		missing_count    INTEGER NOT NULL DEFAULT 0,
		snapshot_version INTEGER NOT NULL DEFAULT 0,
		created_at       TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS inventory_sync_requests (
		id          TEXT PRIMARY KEY,
		customer_id TEXT NOT NULL,
		cluster_id  TEXT NOT NULL,
		operator_id TEXT NOT NULL,
		command_id  TEXT NOT NULL UNIQUE,
		status      TEXT NOT NULL DEFAULT 'pending',
		last_error  TEXT NOT NULL DEFAULT '',
		created_at  TEXT NOT NULL,
		updated_at  TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_inventory_sync_requests_cluster
	 ON inventory_sync_requests(customer_id, cluster_id, status)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_inventory_sync_requests_active_cluster
	 ON inventory_sync_requests(customer_id, cluster_id)
	 WHERE status IN ('pending', 'running')`,

	// Release bundles (REQ-011)
	`CREATE TABLE IF NOT EXISTS release_bundles (
		id             TEXT PRIMARY KEY,
		name           TEXT NOT NULL DEFAULT '',
		digest_alg     TEXT NOT NULL DEFAULT 'sha256',
		digest_value   TEXT NOT NULL DEFAULT '',
		status         TEXT NOT NULL DEFAULT 'received',
		chart_ref      TEXT NOT NULL DEFAULT '',
		chart_version  TEXT NOT NULL DEFAULT '',
		chart_digest   TEXT NOT NULL DEFAULT '',
		images         TEXT NOT NULL DEFAULT '[]',
		git_commit     TEXT NOT NULL DEFAULT '',
		pipeline_id    TEXT NOT NULL DEFAULT '',
		signature_ref  TEXT NOT NULL DEFAULT '',
		sbom_ref       TEXT NOT NULL DEFAULT '',
		provenance_ref TEXT NOT NULL DEFAULT '',
		created_at     TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_release_bundles_digest ON release_bundles(digest_alg, digest_value)`,

	// Artifact preflight results (REQ-045)
	`CREATE TABLE IF NOT EXISTS preflight_results (
		id                  TEXT PRIMARY KEY,
		operation_id        TEXT NOT NULL,
		routing_version     TEXT NOT NULL DEFAULT '',
		bundle_digest       TEXT NOT NULL,
		trust_policy_version TEXT NOT NULL DEFAULT '',
		sbom_policy_version TEXT NOT NULL DEFAULT '',
		result_json         BLOB NOT NULL,
		created_at          TEXT NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_preflight_results_key ON preflight_results(operation_id, routing_version, bundle_digest, trust_policy_version, sbom_policy_version)`,

	// Trust, scan, and exception state must remain available during the SQLite rollback window.
	`CREATE TABLE IF NOT EXISTS trust_roots (
		id              TEXT PRIMARY KEY,
		environment     TEXT NOT NULL,
		key_id          TEXT NOT NULL DEFAULT '',
		public_key_pem  TEXT NOT NULL DEFAULT '',
		issuer          TEXT NOT NULL DEFAULT '',
		subject_pattern TEXT NOT NULL DEFAULT '',
		state           TEXT NOT NULL,
		valid_from      TEXT NOT NULL,
		grace_until     TEXT,
		created_at      TEXT NOT NULL,
		updated_at      TEXT NOT NULL,
		revoked_at      TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_trust_roots_environment ON trust_roots(environment, created_at)`,
	`CREATE TABLE IF NOT EXISTS trust_policies (
		environment      TEXT PRIMARY KEY,
		version          INTEGER NOT NULL DEFAULT 0,
		revocation_epoch INTEGER NOT NULL DEFAULT 0,
		updated_at       TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS scan_results (
		id              TEXT PRIMARY KEY,
		artifact_digest TEXT NOT NULL,
		sbom_ref        TEXT NOT NULL DEFAULT '',
		scanner         TEXT NOT NULL DEFAULT '',
		result_version  TEXT NOT NULL DEFAULT '',
		severity_json   BLOB NOT NULL,
		findings_json   BLOB NOT NULL,
		scanned_at      TEXT NOT NULL,
		created_at      TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_scan_results_artifact_scanner ON scan_results(artifact_digest, scanner, created_at DESC)`,
	`CREATE TABLE IF NOT EXISTS vulnerability_exceptions (
		id              TEXT PRIMARY KEY,
		finding_id      TEXT NOT NULL DEFAULT '',
		artifact_digest TEXT NOT NULL DEFAULT '',
		actor           TEXT NOT NULL DEFAULT '',
		reason          TEXT NOT NULL DEFAULT '',
		expires_at      TEXT NOT NULL,
		created_at      TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_vulnerability_exceptions_artifact ON vulnerability_exceptions(artifact_digest, created_at DESC)`,

	// Artifact lifecycle (REQ-069) — ALTER TABLEs are idempotent (migrate() skips "duplicate column").
	`ALTER TABLE release_bundles ADD COLUMN archived_at TEXT`,
	`ALTER TABLE release_bundles ADD COLUMN archived_from_status TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE release_definitions ADD COLUMN current_bundle_id TEXT`,

	// Candidate artifacts (REQ-069)
	`CREATE TABLE IF NOT EXISTS candidate_artifacts (
		id            TEXT PRIMARY KEY,
		artifact_type TEXT NOT NULL CHECK (artifact_type IN ('image', 'chart')),
		ref           TEXT NOT NULL,
		digest        TEXT NOT NULL,
		bundle_id     TEXT,
		created_at    TEXT NOT NULL,
		UNIQUE(digest, artifact_type)
	)`,
	`ALTER TABLE candidate_artifacts ADD COLUMN validated_at TEXT`,
	`ALTER TABLE candidate_artifacts ADD COLUMN source_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE candidate_artifacts ADD COLUMN orphaned_at TEXT`,
	`CREATE TABLE IF NOT EXISTS bundle_candidate_artifacts (
		bundle_id   TEXT NOT NULL REFERENCES release_bundles(id) ON DELETE CASCADE,
		artifact_id TEXT NOT NULL REFERENCES candidate_artifacts(id) ON DELETE CASCADE,
		linked_at   TEXT NOT NULL,
		orphaned_at TEXT,
		PRIMARY KEY (bundle_id, artifact_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_bundle_candidate_artifacts_artifact ON bundle_candidate_artifacts(artifact_id)`,

	// Emergency changes and explicit convergence (REQ-032).
	`CREATE TABLE IF NOT EXISTS emergency_intents (
		id                    TEXT PRIMARY KEY,
		release_definition_id TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE RESTRICT,
		operation_id          TEXT NOT NULL UNIQUE REFERENCES operations(id) ON DELETE RESTRICT,
		command_id            TEXT NOT NULL UNIQUE,
		action                TEXT NOT NULL CHECK (action IN ('set_container_image','set_replicas','set_approved_annotation')),
		workload_kind         TEXT NOT NULL,
		workload_name         TEXT NOT NULL,
		workload_namespace    TEXT NOT NULL,
		workload_uid          TEXT NOT NULL,
		container             TEXT,
		artifact_id           TEXT,
		image_reference       TEXT,
		target_replicas       INTEGER,
		annotation_scope      TEXT,
		annotation_entries    BLOB,
		convergence           TEXT NOT NULL DEFAULT 'require_promotion' CHECK (convergence IN ('require_promotion','revert_on_next_reconcile')),
		promotion_paths       BLOB,
		before_snapshot       BLOB,
		after_snapshot        BLOB,
		delivery_status       TEXT NOT NULL DEFAULT 'pending' CHECK (delivery_status IN ('pending','queued','delivered','persisted')),
		effect_status         TEXT NOT NULL DEFAULT 'UNKNOWN' CHECK (effect_status IN ('UNKNOWN','APPLIED','NOT_APPLIED')),
		last_delivery_at      TEXT,
		created_at            TEXT NOT NULL,
		updated_at            TEXT NOT NULL
	)`,
	`ALTER TABLE emergency_intents ADD COLUMN effect_status TEXT NOT NULL DEFAULT 'UNKNOWN' CHECK (effect_status IN ('UNKNOWN','APPLIED','NOT_APPLIED'))`,
	`CREATE INDEX IF NOT EXISTS idx_ei_operation ON emergency_intents(operation_id)`,
	`CREATE INDEX IF NOT EXISTS idx_ei_command ON emergency_intents(command_id)`,
	`CREATE INDEX IF NOT EXISTS idx_ei_definition ON emergency_intents(release_definition_id, created_at DESC)`,
	`DROP INDEX IF EXISTS idx_ei_active_locks`,
	`CREATE INDEX IF NOT EXISTS idx_ei_active_locks ON emergency_intents(release_definition_id, workload_kind, workload_name) WHERE effect_status = 'UNKNOWN'`,
	`CREATE TABLE IF NOT EXISTS convergence_tasks (
		id                     TEXT PRIMARY KEY,
		operation_id           TEXT NOT NULL UNIQUE REFERENCES operations(id) ON DELETE RESTRICT,
		release_definition_id  TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE RESTRICT,
		action                 TEXT NOT NULL,
		target_summary         TEXT NOT NULL,
		reason                 TEXT NOT NULL,
		promotion_paths        BLOB NOT NULL,
		status                 TEXT NOT NULL DEFAULT 'pending_promotion' CHECK (status IN ('pending_promotion','converged')),
		active_revision_id     TEXT,
		active_revision_status TEXT,
		last_rejection_reason  TEXT,
		submitted_at           TEXT NOT NULL,
		converged_at           TEXT,
		created_at             TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_ct_definition ON convergence_tasks(release_definition_id, status)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS ux_ct_op ON convergence_tasks(operation_id)`,
	`CREATE TABLE IF NOT EXISTS convergence_prepare_sessions (
		token_hash            TEXT PRIMARY KEY,
		actor_user_id         TEXT NOT NULL,
		organization_id       TEXT NOT NULL,
		release_definition_id TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE RESTRICT,
		parent_revision_id    TEXT REFERENCES values_revisions(id) ON DELETE RESTRICT,
		parent_version        INTEGER NOT NULL,
		task_ids              BLOB NOT NULL,
		locked_paths          BLOB NOT NULL,
		locked_path_hash      TEXT NOT NULL,
		expires_at            TEXT NOT NULL,
		consumed_at           TEXT,
		created_at            TEXT NOT NULL,
		CHECK ((parent_revision_id IS NULL AND parent_version = 0)
			OR (parent_revision_id IS NOT NULL AND parent_version > 0))
	)`,
	`CREATE INDEX IF NOT EXISTS ix_cps_expiry ON convergence_prepare_sessions(expires_at)`,
	// Preflight lifecycle results (REQ-019) — distinct from the cache-based preflight_results table.
	`CREATE TABLE IF NOT EXISTS preflight_lifecycles (
		id                    TEXT PRIMARY KEY,
		operation_id          TEXT UNIQUE,
		operation_terminal_at TEXT,
		stages                TEXT NOT NULL DEFAULT '',
		overall               TEXT NOT NULL DEFAULT 'running',
		created_at            TEXT NOT NULL,
		updated_at            TEXT NOT NULL,
		CHECK (overall IN ('running','passed','failed','cancelled'))
	)`,
	`CREATE INDEX IF NOT EXISTS idx_preflight_lifecycles_terminal ON preflight_lifecycles(operation_terminal_at)`,
	// Durable authorization source, policy, grants, rules, and consumer checkpoints (REQ-027).
	`CREATE TABLE IF NOT EXISTS authorization_source_version (
		id      INTEGER PRIMARY KEY CHECK (id = 1),
		version INTEGER NOT NULL DEFAULT 0
	)`,
	`INSERT OR IGNORE INTO authorization_source_version (id, version) VALUES (1, 0)`,
	`CREATE TABLE IF NOT EXISTS capability_grants (
		organization_id TEXT NOT NULL,
		subject         TEXT NOT NULL,
		action          TEXT NOT NULL,
		granted_by      TEXT NOT NULL,
		revoked         INTEGER NOT NULL DEFAULT 0,
		created_at      TEXT NOT NULL,
		updated_at      TEXT NOT NULL,
		PRIMARY KEY (organization_id, subject, action)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_capability_grants_active ON capability_grants(organization_id, subject, action) WHERE revoked = 0`,
	`CREATE TABLE IF NOT EXISTS casbin_rule (
		id    INTEGER PRIMARY KEY AUTOINCREMENT,
		ptype TEXT NOT NULL,
		v0    TEXT NOT NULL DEFAULT '',
		v1    TEXT NOT NULL DEFAULT '',
		v2    TEXT NOT NULL DEFAULT '',
		v3    TEXT NOT NULL DEFAULT '',
		v4    TEXT NOT NULL DEFAULT '',
		v5    TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_casbin_rule_lookup ON casbin_rule(ptype, v0, v1, v2, v3)`,
	`CREATE TABLE IF NOT EXISTS policy_version (
		id      INTEGER PRIMARY KEY CHECK (id = 1),
		version INTEGER NOT NULL DEFAULT 0
	)`,
	`INSERT OR IGNORE INTO policy_version (id, version) VALUES (1, 0)`,
	`CREATE TABLE IF NOT EXISTS authorization_checkpoints (
		organization_id TEXT NOT NULL,
		customer_id     TEXT NOT NULL,
		source_version  INTEGER NOT NULL DEFAULT 0,
		policy_version  INTEGER NOT NULL DEFAULT 0,
		fresh           INTEGER NOT NULL DEFAULT 0,
		updated_at      TEXT NOT NULL,
		PRIMARY KEY (organization_id, customer_id)
	)`,
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }
