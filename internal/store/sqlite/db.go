// Package sqlite provides a SQLite-backed implementation of the store interfaces.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
	_ "modernc.org/sqlite"
)

// Store implements store.Store backed by SQLite.
type Store struct {
	db              *sql.DB
	ops             *operationStore
	operationEvents *operationEventStore
	defs            *definitionStore
	vals            *valuesStore
	customers       *customerStore
	clusters        *clusterStore
	tokens          *enrollmentTokenStore
	operators       *operatorStore
	sessions        *sessionStore
	outbox          *outboxStore
	users           *userStore
	authSess        *authSessionStore
	orgs            *organizationStore
	orgMembers      *organizationMemberStore
	bindings        *bindingStore
	audit           *auditEventStore
	notif           *notificationStore
	bundles         *bundleStore
	verifs          *verificationStore
	routes          *clusterRouteStore
	invs            *inventoryStore
	custEvents      *customerEventStore
	defEvents       *definitionEventStore
	preflight       *preflightStore
}

// Open creates a new SQLite-backed Store, running migrations on the database.
// The DSN must be a valid modernc.org/sqlite connection string.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
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

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite migrate: %w", err)
	}

	s := &Store{db: db}
	s.ops = &operationStore{db: db}
	s.operationEvents = &operationEventStore{db: db}
	s.defs = &definitionStore{db: db}
	s.defEvents = &definitionEventStore{db: db}
	s.preflight = &preflightStore{db: db}
	s.vals = &valuesStore{db: db}
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
	s.audit = &auditEventStore{db: db}
	s.bundles = &bundleStore{db: db}
	s.invs = &inventoryStore{db: db}
	s.verifs = &verificationStore{db: db}
	s.custEvents = &customerEventStore{db: db}
	s.routes = &clusterRouteStore{db: db}
	return s, nil
}

// Operations returns the OperationStore.
func (s *Store) Operations() store.OperationStore { return s.ops }

// OperationEvents returns the operation state event store.
func (s *Store) OperationEvents() store.OperationEventStore { return s.operationEvents }

// Customers returns the CustomerStore.
func (s *Store) Customers() store.CustomerStore { return s.customers }

// Clusters returns the ClusterStore.
func (s *Store) Clusters() store.ClusterStore { return s.clusters }

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

// Bundles returns the BundleStore.
func (s *Store) Bundles() store.BundleStore { return s.bundles }

// Notifications returns the NotificationStore.
func (s *Store) Notifications() store.NotificationStore { return s.notif }

// Verifications returns the VerificationStore.
func (s *Store) Verifications() store.VerificationStore { return s.verifs }

// CustomerEvents returns the CustomerEventStore.
func (s *Store) CustomerEvents() store.CustomerEventStore { return s.custEvents }

// ClusterRoutes returns the ClusterRouteStore.
func (s *Store) ClusterRoutes() store.ClusterRouteStore { return s.routes }

// Inventories returns the InventoryStore.
func (s *Store) Inventories() store.InventoryStore { return s.invs }

// Close closes the underlying database connection.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying *sql.DB for testing.
func (s *Store) DB() *sql.DB { return s.db }

// OpenTest creates a Store backed by an in-memory SQLite database for testing.
// The caller is responsible for closing the store via t.Cleanup.
func OpenTest(t interface{ Cleanup(func()) }) *Store {
	st, err := Open("file::memory:?cache=shared")
	if err != nil {
		panic("sqlite OpenTest: " + err.Error())
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// migrate runs the ordered migration steps against the database.
// ALTER TABLE ADD COLUMN statements that fail because the column already
// exists are silently skipped to keep migrations idempotent.
func migrate(db *sql.DB) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback on a committed transaction is a no-op; error is irrelevant here.

	for _, stmt := range migrationStatements {
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
		release_definition_id TEXT NOT NULL REFERENCES release_definitions(id) ON DELETE CASCADE,
		revision              INTEGER NOT NULL DEFAULT 1,
		status                TEXT NOT NULL DEFAULT 'draft',
		"values"              BLOB NOT NULL,
		digest                TEXT NOT NULL DEFAULT '',
		parent_revision_id    TEXT NOT NULL DEFAULT '',
		secret_refs           BLOB,
		created_at            TEXT NOT NULL,
		updated_at            TEXT NOT NULL
	)`,

	// Migration: add digest, parent_revision_id, secret_refs to existing tables.
	`ALTER TABLE values_revisions ADD COLUMN digest TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE values_revisions ADD COLUMN parent_revision_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE values_revisions ADD COLUMN secret_refs BLOB`,

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

	`CREATE TABLE IF NOT EXISTS clusters (
		id             TEXT PRIMARY KEY,
		name           TEXT NOT NULL,
		customer_id    TEXT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
		kubeconfig_ref TEXT NOT NULL DEFAULT '',
		status         TEXT NOT NULL DEFAULT 'active',
		created_at     TEXT NOT NULL,
		updated_at     TEXT NOT NULL
	)`,

	`CREATE INDEX IF NOT EXISTS idx_clusters_customer ON clusters(customer_id)`,

	// Operator enrollment and session management (REQ-015, REQ-044)
	`CREATE TABLE IF NOT EXISTS enrollment_tokens (
		id          TEXT PRIMARY KEY,
		customer_id TEXT NOT NULL,
		cluster_id  TEXT NOT NULL,
		token       TEXT NOT NULL UNIQUE,
		created_at  TEXT NOT NULL,
		expires_at  TEXT NOT NULL,
		used        INTEGER NOT NULL DEFAULT 0,
		used_at     TEXT,
		operator_id TEXT NOT NULL DEFAULT ''
	)`,

	// REQ-015: token_hash column for enrollment token security
	`ALTER TABLE enrollment_tokens ADD COLUMN token_hash TEXT NOT NULL DEFAULT ''`,

	`CREATE TABLE IF NOT EXISTS operators (
		id            TEXT PRIMARY KEY,
		customer_id   TEXT NOT NULL,
		cluster_id    TEXT NOT NULL,
		operator_name TEXT NOT NULL DEFAULT '',
		cert_serial   TEXT NOT NULL,
		status        TEXT NOT NULL DEFAULT 'active',
		superseded_by TEXT NOT NULL DEFAULT '',
		revoked_at    TEXT,
		created_at    TEXT NOT NULL,
		updated_at    TEXT NOT NULL
	)`,

	`CREATE INDEX IF NOT EXISTS idx_operators_cert ON operators(cert_serial)`,
	`CREATE INDEX IF NOT EXISTS idx_operators_name ON operators(operator_name)`,
	`CREATE INDEX IF NOT EXISTS idx_operators_cluster ON operators(cluster_id, status)`,

	// REQ-015: operator_name column for existing databases
	`ALTER TABLE operators ADD COLUMN operator_name TEXT NOT NULL DEFAULT ''`,

	`CREATE TABLE IF NOT EXISTS sessions (
		id             TEXT PRIMARY KEY,
		operator_id    TEXT NOT NULL REFERENCES operators(id) ON DELETE CASCADE,
		status         TEXT NOT NULL DEFAULT 'online',
		started_at     TEXT NOT NULL,
		last_heartbeat TEXT NOT NULL,
		expires_at     TEXT NOT NULL
	)`,

	`ALTER TABLE sessions ADD COLUMN instance_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE sessions ADD COLUMN version TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE sessions ADD COLUMN capabilities TEXT NOT NULL DEFAULT '{}'`,
	`ALTER TABLE sessions ADD COLUMN active_config_version TEXT NOT NULL DEFAULT ''`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_instance ON sessions(operator_id, instance_id)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_one_active_operator ON sessions(operator_id) WHERE status IN ('online', 'suspect')`,

	`CREATE INDEX IF NOT EXISTS idx_sessions_operator ON sessions(operator_id, status)`,

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
	// Auth & RBAC — users, sessions, organizations, bindings (REQ-025, REQ-026, REQ-049)
	`CREATE TABLE IF NOT EXISTS users (
		id            TEXT PRIMARY KEY,
		username      TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		status        TEXT NOT NULL DEFAULT 'active',
		created_at    TEXT NOT NULL,
		updated_at    TEXT NOT NULL
	)`,

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
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }
