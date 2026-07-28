// Package postgres implements the store.Store contracts on PostgreSQL.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/ndzuki/release-manager/internal/config"
	infrastructure "github.com/ndzuki/release-manager/internal/postgres"
	"github.com/ndzuki/release-manager/internal/store"
	"gorm.io/gorm"
)

// DB wraps a GORM session while exposing the database/sql-shaped methods used
// by the Store implementations. SQL executes through GORM's current ConnPool,
// including the transaction connection created by BeginTx.
type DB struct{ gorm *gorm.DB }
type Tx struct{ gorm *gorm.DB }

var placeholderPattern = regexp.MustCompile(`\?`)

func postgresQuery(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	i := 0
	return placeholderPattern.ReplaceAllStringFunc(query, func(string) string {
		i++
		return fmt.Sprintf("$%d", i)
	})
}

func (db *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result := db.gorm.WithContext(ctx).Exec(postgresQuery(query), args...)
	return gormResult{result: result}, result.Error
}

func (db *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return db.gorm.WithContext(ctx).Raw(postgresQuery(query), args...).Rows()
}

func (db *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return db.gorm.WithContext(ctx).Raw(postgresQuery(query), args...).Row()
}

func (db *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	tx := db.gorm.WithContext(ctx).Begin(opts)
	if tx.Error != nil {
		return nil, tx.Error
	}
	return &Tx{gorm: tx}, nil
}

func (db *DB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	connPool, ok := db.gorm.WithContext(ctx).Statement.ConnPool.(interface {
		PrepareContext(context.Context, string) (*sql.Stmt, error)
	})
	if !ok {
		return nil, fmt.Errorf("prepare: unsupported GORM connection pool")
	}
	return connPool.PrepareContext(ctx, postgresQuery(query))
}

func (tx *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result := tx.gorm.WithContext(ctx).Exec(postgresQuery(query), args...)
	return gormResult{result: result}, result.Error
}

func (tx *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.gorm.WithContext(ctx).Raw(postgresQuery(query), args...).Rows()
}

func (tx *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.gorm.WithContext(ctx).Raw(postgresQuery(query), args...).Row()
}

func (tx *Tx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	connPool, ok := tx.gorm.WithContext(ctx).Statement.ConnPool.(interface {
		PrepareContext(context.Context, string) (*sql.Stmt, error)
	})
	if !ok {
		return nil, fmt.Errorf("prepare: unsupported GORM transaction pool")
	}
	return connPool.PrepareContext(ctx, postgresQuery(query))
}

func (tx *Tx) Commit() error   { return tx.gorm.Commit().Error }
func (tx *Tx) Rollback() error { return tx.gorm.Rollback().Error }

type gormResult struct{ result *gorm.DB }

func (r gormResult) LastInsertId() (int64, error) {
	return 0, fmt.Errorf("last insert id is unsupported")
}
func (r gormResult) RowsAffected() (int64, error) { return r.result.RowsAffected, r.result.Error }

type valuesApprovalStore struct{ gorm *DB }
type auditExportStore struct{ gorm *DB }
type trustRootStore struct{ gorm *DB }
type scanResultStore struct{ gorm *DB }
type vulnerabilityExceptionStore struct{ gorm *DB }
type candidateArtifactStore struct{ gorm *DB }
type preflightLifecycleStore struct{ gorm *DB }

// Store implements store.Store backed by PostgreSQL.
type Store struct {
	sqlDB             *sql.DB
	db                *DB
	gormDB            *gorm.DB
	ops               *operationStore
	operationEvents   *operationEventStore
	defs              *definitionStore
	vals              *valuesStore
	valuesApproval    *valuesApprovalStore
	customers         *customerStore
	clusters          *clusterStore
	tokens            *enrollmentTokenStore
	operators         *operatorStore
	sessions          *sessionStore
	outbox            *outboxStore
	users             *userStore
	authSess          *authSessionStore
	orgs              *organizationStore
	orgMembers        *organizationMemberStore
	bindings          *bindingStore
	audit             *auditEventStore
	notif             *notificationStore
	auditExports      *auditExportStore
	bundles           *bundleStore
	verifs            *verificationStore
	routes            *clusterRouteStore
	invs              *inventoryStore
	syncRequests      *inventorySyncRequestStore
	trustRoots        *trustRootStore
	scanResults       *scanResultStore
	vulnExceptions    *vulnerabilityExceptionStore
	custEvents        *customerEventStore
	defEvents         *definitionEventStore
	preflight         *preflightStore
	candidateArts     *candidateArtifactStore
	artifactEvents    *artifactEventStore
	validationOutbox  *validationOutboxStore
	bundleSubmissions *bundleSubmissionStore
	eventSubmissions  *artifactEventSubmissionStore
	preflightCycles   *preflightLifecycleStore
	closeOnce         sync.Once
	closeErr          error
}

// New constructs a Store over the supplied shared database/sql pool and GORM wrapper.
// Open initializes the shared PostgreSQL database, applies migrations, and
// constructs the Store. See ADR-070-shared-postgresql-pool-and-transaction-seam.
func Open(ctx context.Context, cfg config.DatabaseConfig, migrationFS fs.FS) (*Store, error) {
	database, err := infrastructure.Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := infrastructure.RunMigrations(ctx, database.SQLDB(), migrationFS); err != nil {
		_ = database.Close()
		return nil, err
	}
	st, err := New(database.SQLDB(), database.GORM())
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	return st, nil
}

func New(sqlDB *sql.DB, gormDB *gorm.DB) (*Store, error) {
	if sqlDB == nil || gormDB == nil {
		return nil, fmt.Errorf("store: nil PostgreSQL database")
	}
	s := &Store{sqlDB: sqlDB, db: &DB{gorm: gormDB}, gormDB: gormDB}
	s.ops = &operationStore{gorm: s.db}
	s.operationEvents = &operationEventStore{gorm: s.db}
	s.defs = &definitionStore{gorm: s.db}
	s.defEvents = &definitionEventStore{gorm: s.db}
	s.preflight = &preflightStore{gorm: s.db}
	s.vals = &valuesStore{gorm: s.db}
	s.valuesApproval = &valuesApprovalStore{gorm: s.db}
	s.customers = &customerStore{gorm: s.db}
	s.clusters = &clusterStore{gorm: s.db}
	s.tokens = &enrollmentTokenStore{gorm: s.db}
	s.operators = &operatorStore{gorm: s.db}
	s.sessions = &sessionStore{gorm: s.db}
	s.outbox = &outboxStore{gorm: s.db}
	s.users = &userStore{gorm: s.db}
	s.authSess = &authSessionStore{gorm: s.db}
	s.orgs = &organizationStore{gorm: s.db}
	s.orgMembers = &organizationMemberStore{gorm: s.db}
	s.bindings = &bindingStore{gorm: s.db}
	s.notif = &notificationStore{gorm: s.db}
	s.audit = &auditEventStore{gorm: s.db}
	s.bundles = &bundleStore{gorm: s.db}
	s.artifactEvents = &artifactEventStore{gorm: s.db}
	s.validationOutbox = &validationOutboxStore{gorm: s.db}
	s.auditExports = &auditExportStore{gorm: s.db}
	s.invs = &inventoryStore{gorm: s.db}
	s.syncRequests = &inventorySyncRequestStore{gorm: s.db}
	s.verifs = &verificationStore{gorm: s.db}
	s.custEvents = &customerEventStore{gorm: s.db}
	s.trustRoots = &trustRootStore{gorm: s.db}
	s.scanResults = &scanResultStore{gorm: s.db}
	s.vulnExceptions = &vulnerabilityExceptionStore{gorm: s.db}
	s.routes = &clusterRouteStore{gorm: s.db}
	s.candidateArts = &candidateArtifactStore{gorm: s.db}
	s.bundleSubmissions = &bundleSubmissionStore{
		gorm: gormDB, bundles: s.bundles, candidates: s.candidateArts, validation: s.validationOutbox,
	}
	s.eventSubmissions = &artifactEventSubmissionStore{
		gorm: gormDB, events: s.artifactEvents, candidates: s.candidateArts,
	}
	s.preflightCycles = &preflightLifecycleStore{gorm: s.db}
	return s, nil
}

var _ store.Store = (*Store)(nil)
var (
	_ store.ValuesApprovalStore          = (*valuesApprovalStore)(nil)
	_ store.ValuesApprovalReader         = (*valuesApprovalStore)(nil)
	_ store.AuditExportStore             = (*auditExportStore)(nil)
	_ store.TrustRootStore               = (*trustRootStore)(nil)
	_ store.ScanResultStore              = (*scanResultStore)(nil)
	_ store.VulnerabilityExceptionStore  = (*vulnerabilityExceptionStore)(nil)
	_ store.CandidateArtifactStore       = (*candidateArtifactStore)(nil)
	_ store.ArtifactEventStore           = (*artifactEventStore)(nil)
	_ store.ValidationOutboxStore        = (*validationOutboxStore)(nil)
	_ store.BundleSubmissionStore        = (*bundleSubmissionStore)(nil)
	_ store.ArtifactEventSubmissionStore = (*artifactEventSubmissionStore)(nil)
	_ store.PreflightLifecycleStore      = (*preflightLifecycleStore)(nil)
	_ store.InventorySyncRequestStore    = (*inventorySyncRequestStore)(nil)
)

func (s *Store) Operations() store.OperationStore                           { return s.ops }
func (s *Store) OperationEvents() store.OperationEventStore                 { return s.operationEvents }
func (s *Store) Customers() store.CustomerStore                             { return s.customers }
func (s *Store) Clusters() store.ClusterStore                               { return s.clusters }
func (s *Store) EnrollmentTokens() store.EnrollmentTokenStore               { return s.tokens }
func (s *Store) Operators() store.OperatorStore                             { return s.operators }
func (s *Store) Sessions() store.SessionStore                               { return s.sessions }
func (s *Store) Outbox() store.OutboxStore                                  { return s.outbox }
func (s *Store) Definitions() store.DefinitionStore                         { return s.defs }
func (s *Store) DefinitionEvents() store.DefinitionEventStore               { return s.defEvents }
func (s *Store) PreflightResults() store.PreflightStore                     { return s.preflight }
func (s *Store) Values() store.ValuesStore                                  { return s.vals }
func (s *Store) Users() store.UserStore                                     { return s.users }
func (s *Store) AuthSessions() store.AuthSessionStore                       { return s.authSess }
func (s *Store) Organizations() store.OrganizationStore                     { return s.orgs }
func (s *Store) OrgMembers() store.OrganizationMemberStore                  { return s.orgMembers }
func (s *Store) Bindings() store.BindingStore                               { return s.bindings }
func (s *Store) AuditEvents() store.AuditEventStore                         { return s.audit }
func (s *Store) Bundles() store.BundleStore                                 { return s.bundles }
func (s *Store) Notifications() store.NotificationStore                     { return s.notif }
func (s *Store) Verifications() store.VerificationStore                     { return s.verifs }
func (s *Store) CustomerEvents() store.CustomerEventStore                   { return s.custEvents }
func (s *Store) ClusterRoutes() store.ClusterRouteStore                     { return s.routes }
func (s *Store) Inventories() store.InventoryStore                          { return s.invs }
func (s *Store) InventorySyncRequests() store.InventorySyncRequestStore      { return s.syncRequests }
func (s *Store) ValuesApproval() store.ValuesApprovalStore                  { return s.valuesApproval }
func (s *Store) ValuesApprovalEvidence() store.ValuesApprovalReader         { return s.valuesApproval }
func (s *Store) AuditExports() store.AuditExportStore                       { return s.auditExports }
func (s *Store) TrustRoots() store.TrustRootStore                           { return s.trustRoots }
func (s *Store) ScanResults() store.ScanResultStore                         { return s.scanResults }
func (s *Store) VulnerabilityExceptions() store.VulnerabilityExceptionStore { return s.vulnExceptions }
func (s *Store) CandidateArtifacts() store.CandidateArtifactStore           { return s.candidateArts }
func (s *Store) ArtifactEvents() store.ArtifactEventStore                   { return s.artifactEvents }
func (s *Store) ValidationOutbox() store.ValidationOutboxStore              { return s.validationOutbox }
func (s *Store) BundleSubmissions() store.BundleSubmissionStore             { return s.bundleSubmissions }
func (s *Store) ArtifactEventSubmissions() store.ArtifactEventSubmissionStore {
	return s.eventSubmissions
}
func (s *Store) PreflightLifecycles() store.PreflightLifecycleStore { return s.preflightCycles }

func (s *Store) Close() error {
	if s == nil || s.sqlDB == nil {
		return nil
	}
	s.closeOnce.Do(func() { s.closeErr = s.sqlDB.Close() })
	return s.closeErr
}

func (s *Store) DB() *DB        { return s.db }
func (s *Store) SQLDB() *sql.DB { return s.sqlDB }
func (s *Store) GORM() *gorm.DB { return s.gormDB }

// IsUniqueConstraint reports PostgreSQL unique/exclusion violations.
func isUniqueConstraint(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23P01")
}

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }
