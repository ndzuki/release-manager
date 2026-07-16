// Package store defines the persistence layer interfaces and domain types
// for the release-manager core pipeline.
package store

import (
	"context"
	"errors"
	"time"
)

// VerificationStatus is the outcome of an artifact trust verification.
type VerificationStatus string

const (
	VerificationTrusted                 VerificationStatus = "trusted"
	VerificationRejected                VerificationStatus = "rejected"
	VerificationPolicyWarning           VerificationStatus = "policy_warning"
	VerificationSignatureMissing        VerificationStatus = "signature_missing"
	VerificationVerificationUnavailable VerificationStatus = "verification_unavailable"
)

// Sentinel errors for store operations.
var (
	ErrNotFound       = errors.New("store: not found")
	ErrOptimisticLock = errors.New("store: optimistic lock conflict")
	ErrDuplicateKey   = errors.New("store: duplicate key")
)

// OperationType classifies the kind of release operation.
type OperationType string

const (
	OperationInstall   OperationType = "INSTALL"
	OperationUpgrade   OperationType = "UPGRADE"
	OperationRollback  OperationType = "ROLLBACK"
	OperationEmergency OperationType = "EMERGENCY"
)

func (t OperationType) Valid() bool {
	switch t {
	case OperationInstall, OperationUpgrade, OperationRollback, OperationEmergency:
		return true
	}
	return false
}

// IsStandard returns true for non-EMERGENCY operation types.
func (t OperationType) IsStandard() bool {
	return t == OperationInstall || t == OperationUpgrade || t == OperationRollback
}

// OperationStatus is the finite state of an operation lifecycle.
type OperationStatus string

const (
	StatusPending    OperationStatus = "pending"
	StatusPreflight  OperationStatus = "preflight"
	StatusQueued     OperationStatus = "queued"
	StatusRunning    OperationStatus = "running"
	StatusCancelling OperationStatus = "cancelling"
	StatusSucceeded  OperationStatus = "succeeded"
	StatusFailed     OperationStatus = "failed"
	StatusCancelled  OperationStatus = "cancelled"
	StatusTimeout    OperationStatus = "timeout"
)

// IsTerminal returns true if the status is a final state.
func (s OperationStatus) IsTerminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusTimeout:
		return true
	}
	return false
}

// DefinitionStatus is the lifecycle of a release definition.
type DefinitionStatus string

const (
	DefStatusDraft    DefinitionStatus = "draft"
	DefStatusActive   DefinitionStatus = "active"
	DefStatusDisabled DefinitionStatus = "disabled"
)

// ValuesStatus is the approval state of a values revision.
type ValuesStatus string

const (
	ValuesStatusDraft    ValuesStatus = "draft"
	ValuesStatusApproved ValuesStatus = "approved"
	ValuesStatusRejected ValuesStatus = "rejected"
)

// ActorContext records who initiated an operation.
type ActorContext struct {
	UserID       string `json:"user_id"`
	Organization string `json:"organization"`
}

// Operation is the core domain object representing a release operation.
type Operation struct {
	ID                  string          `json:"id"`
	OperationType       OperationType   `json:"operation_type"`
	Status              OperationStatus `json:"status"`
	ReleaseDefinitionID string          `json:"release_definition_id"`
	IdempotencyKey      string          `json:"idempotency_key"`
	RequestHash         string          `json:"request_hash"`
	StateVersion        int             `json:"state_version"`
	BundleID            string          `json:"bundle_id"`
	ValuesRevisionID    string          `json:"values_revision_id"`
	ExpectedRevision    int             `json:"expected_revision"`
	ValuesPatch         []byte          `json:"values_patch,omitempty"`
	Actor               ActorContext    `json:"actor"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
	Deadline            *time.Time      `json:"deadline,omitempty"`
	LastError           string          `json:"last_error,omitempty"`
}

// ReleaseDefinition represents a Helm release target configuration.
type ReleaseDefinition struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	CustomerID        string           `json:"customer_id"`
	ClusterID         string           `json:"cluster_id"`
	Namespace         string           `json:"namespace"`
	ReleaseName       string           `json:"release_name"`
	ChartName         string           `json:"chart_name"`
	Status            DefinitionStatus `json:"status"`
	OptimisticVersion int              `json:"optimistic_version"`
	CreatedBy         string           `json:"created_by"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

// ValuesRevision stores the desired configuration for a release target.
type ValuesRevision struct {
	ID                  string       `json:"id"`
	ReleaseDefinitionID string       `json:"release_definition_id"`
	Revision            int          `json:"revision"`
	Status              ValuesStatus `json:"status"`
	Values              []byte       `json:"values"`
	Digest              string       `json:"digest"`
	ParentRevisionID    string       `json:"parent_revision_id"`
	SecretRefs          []byte       `json:"secret_refs,omitempty"`
	CreatedAt           time.Time    `json:"created_at"`
	UpdatedAt           time.Time    `json:"updated_at"`
}

// CustomerStatus is the lifecycle state of a customer tenant.
type CustomerStatus string

const (
	CustomerActive   CustomerStatus = "active"
	CustomerDisabled CustomerStatus = "disabled"
)

// ClusterStatus is the lifecycle state of a target cluster.
type ClusterStatus string

const (
	ClusterActive   ClusterStatus = "active"
	ClusterDisabled ClusterStatus = "disabled"
)

// OperatorStatus indicates the enrollment state of an operator.
type OperatorStatus string

const (
	OperatorActive     OperatorStatus = "active"
	OperatorSuperseded OperatorStatus = "superseded"
	OperatorRevoked    OperatorStatus = "revoked"
)

// SessionStatus tracks the operator connection lifecycle.
type SessionStatus string

const (
	SessionOnline  SessionStatus = "online"
	SessionSuspect SessionStatus = "suspect"
	SessionOffline SessionStatus = "offline"
)

// CommandStatus is the outbox delivery and execution state.
type CommandStatus string

const (
	CommandPending   CommandStatus = "pending"
	CommandDelivered CommandStatus = "delivered"
	CommandPersisted CommandStatus = "persisted"
	CommandRunning   CommandStatus = "running"
	CommandSucceeded CommandStatus = "succeeded"
	CommandFailed    CommandStatus = "failed"
)

// Customer represents a tenant in the release-manager.
type Customer struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Slug      string         `json:"slug"`
	Status    CustomerStatus `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// Cluster represents a target Kubernetes cluster belonging to a customer.
type Cluster struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	CustomerID    string        `json:"customer_id"`
	KubeconfigRef string        `json:"kubeconfig_ref"`
	Status        ClusterStatus `json:"status"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// EnrollmentToken is a single-use token for operator registration.
type EnrollmentToken struct {
	ID         string     `json:"id"`
	CustomerID string     `json:"customer_id"`
	ClusterID  string     `json:"cluster_id"`
	Token      string     `json:"token"`
	TokenHash  string     `json:"token_hash"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	Used       bool       `json:"used"`
	UsedAt     *time.Time `json:"used_at,omitempty"`
	OperatorID string     `json:"operator_id,omitempty"`
}

// Operator represents a registered operator agent in a cluster.
type Operator struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	CustomerID   string         `json:"customer_id"`
	ClusterID    string         `json:"cluster_id"`
	CertSerial   string         `json:"cert_serial"`
	Status       OperatorStatus `json:"status"`
	SupersededBy string         `json:"superseded_by,omitempty"`
	RevokedAt    *time.Time     `json:"revoked_at,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// Session tracks a live operator connection.
type Session struct {
	ID                  string            `json:"id"`
	OperatorID          string            `json:"operator_id"`
	InstanceID          string            `json:"instance_id"`
	Version             string            `json:"version"`
	Capabilities        map[string]string `json:"capabilities"`
	ActiveConfigVersion string            `json:"active_config_version"`
	Status              SessionStatus     `json:"status"`
	StartedAt           time.Time         `json:"started_at"`
	LastHeartbeat       time.Time         `json:"last_heartbeat"`
	ExpiresAt           time.Time         `json:"expires_at"`
}

// OutboxEntry holds a command pending delivery in the outbox.
type OutboxEntry struct {
	ID            string        `json:"id"`
	CommandID     string        `json:"command_id"` // de-duplication key, independent of operation_id
	OperationID   string        `json:"operation_id"`
	OperationType string        `json:"operation_type"` // INSTALL, UPGRADE, ROLLBACK, etc.
	OperatorID    string        `json:"operator_id"`
	Payload       []byte        `json:"payload"`
	Status        CommandStatus `json:"status"`
	MaxInFlight   int           `json:"max_inflight"`
	Sequence      int64         `json:"sequence"` // global monotonic sequence number
	ResultJSON    string        `json:"result_json,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	DeliveredAt   *time.Time    `json:"delivered_at,omitempty"`
	AckedAt       *time.Time    `json:"acked_at,omitempty"`
}

// --- Auth domain types (REQ-025, REQ-026, REQ-049) ---

// UserStatus is the lifecycle state of a user account.
type UserStatus string

const (
	UserActive   UserStatus = "active"
	UserDisabled UserStatus = "disabled"
)

// Role defines the permission level within an organization.
type Role string

const (
	RolePlatformAdmin Role = "platform_admin"
	RoleReleaseAdmin  Role = "release_admin"
	RoleDeployer      Role = "deployer"
	RoleViewer        Role = "viewer"
)

// Valid returns true if the role is a recognized value.
func (r Role) Valid() bool {
	switch r {
	case RolePlatformAdmin, RoleReleaseAdmin, RoleDeployer, RoleViewer:
		return true
	}
	return false
}

// CanGrant returns true if the current role can grant the target role.
// release_admin cannot grant platform_admin (AC-026-01).
func (r Role) CanGrant(target Role) bool {
	if r == RolePlatformAdmin {
		return true
	}
	if r == RoleReleaseAdmin {
		return target != RolePlatformAdmin
	}
	return false
}

// OrganizationStatus is the lifecycle state of an organization.
type OrganizationStatus string

const (
	OrgActive   OrganizationStatus = "active"
	OrgDisabled OrganizationStatus = "disabled"
)

// BindingStatus is the lifecycle state of an org-customer binding.
type BindingStatus string

const (
	BindingActive  BindingStatus = "active"
	BindingRevoked BindingStatus = "revoked"
)

// User represents a local user account.
type User struct {
	ID           string
	Username     string
	PasswordHash string
	Status       UserStatus
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// AuthSession represents an authenticated session with token family tracking.
type AuthSession struct {
	ID               string
	UserID           string
	TokenFamily      string
	RefreshTokenHash string
	ExpiresAt        time.Time
	CreatedAt        time.Time
	Revoked          bool
}

// Organization represents a tenant organization for RBAC.
type Organization struct {
	ID                string
	Name              string
	Status            OrganizationStatus
	OptimisticVersion int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// OrganizationMember links a user to an organization with a role.
type OrganizationMember struct {
	OrgID             string
	UserID            string
	Role              Role
	OptimisticVersion int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// OrgCustomerBinding grants an organization access to a customer.
type OrgCustomerBinding struct {
	ID                string
	OrgID             string
	CustomerID        string
	Status            BindingStatus
	OptimisticVersion int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// --- Audit & Notification domain types (REQ-050, REQ-031) ---

// AuditActorKind classifies the principal that performed an action.
type AuditActorKind string

const (
	AuditActorUser    AuditActorKind = "user"
	AuditActorService AuditActorKind = "service"
	AuditActorAPIKey  AuditActorKind = "api_key"
	AuditActorSystem  AuditActorKind = "system"
)

// AuditEvent represents a single audit trail entry.
type AuditEvent struct {
	ID             string
	ActorKind      AuditActorKind
	ActorID        string
	OrganizationID string
	Role           string
	ResourceType   string
	ResourceID     string
	Action         string
	Status         string
	DurationMs     int64
	ChangeSummary  string
	Metadata       map[string]string
	CreatedAt      time.Time
}

// NotificationChannel is the delivery channel for a notification.
type NotificationChannel string

const (
	NotificationChannelWebhook NotificationChannel = "webhook"
	NotificationChannelEmail   NotificationChannel = "email"
	NotificationChannelSlack   NotificationChannel = "slack"
)

// NotificationStatus is the delivery lifecycle state.
type NotificationStatus string

const (
	NotificationPending    NotificationStatus = "pending"
	NotificationSending    NotificationStatus = "sending"
	NotificationDelivered  NotificationStatus = "delivered"
	NotificationFailed     NotificationStatus = "failed"
	NotificationDeadLetter NotificationStatus = "dead_letter"
)

// NotificationJob tracks a notification delivery attempt.
type NotificationJob struct {
	ID           string
	OperationID  string
	Channel      NotificationChannel
	Recipient    string
	Status       NotificationStatus
	RetryCount   int
	MaxRetries   int
	NextRetryAt  *time.Time
	LastError    string
	DeadLetterAt *time.Time
	Metadata     map[string]string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// --- Emergency change domain types (REQ-032) ---

// EmergencyAction is a typed operation whitelisted for emergency changes.
type EmergencyAction string

const (
	EmergencySetContainerImage     EmergencyAction = "set_container_image"
	EmergencySetReplicas           EmergencyAction = "set_replicas"
	EmergencySetApprovedAnnotation EmergencyAction = "set_approved_annotation"
)

// Valid returns true if the emergency action is a recognized value.
func (a EmergencyAction) Valid() bool {
	switch a {
	case EmergencySetContainerImage, EmergencySetReplicas, EmergencySetApprovedAnnotation:
		return true
	}
	return false
}

// EmergencyConvergence controls how the system returns to normal state.
type EmergencyConvergence string

const (
	EmergencyRequirePromotion      EmergencyConvergence = "require_promotion"
	EmergencyRevertOnNextReconcile EmergencyConvergence = "revert_on_next_reconcile"
)

// EmergencyPayload carries the typed emergency change request data.
type EmergencyPayload struct {
	Action      EmergencyAction
	Payload     string // JSON-encoded action-specific parameters
	Reason      string
	Convergence EmergencyConvergence
}

// --- Bundle domain types (REQ-011) ---

// BundleStatus is the lifecycle state of a ReleaseBundle.
type BundleStatus string

const (
	BundleReceived  BundleStatus = "received"
	BundleValidated BundleStatus = "validated"
	BundleRejected  BundleStatus = "rejected"
)

// Valid returns true if the status is a recognized value.
func (s BundleStatus) Valid() bool {
	switch s {
	case BundleReceived, BundleValidated, BundleRejected:
		return true
	default:
		return false
	}
}

// BundleImage maps a container image to its Helm values path.
type BundleImage struct {
	Ref        string
	Digest     string
	ValuesPath string
}

// ReleaseBundle represents an immutable release artifact bundle.
type ReleaseBundle struct {
	ID            string
	Name          string
	DigestAlg     string
	DigestValue   string
	Status        BundleStatus
	ChartRef      string
	ChartVersion  string
	ChartDigest   string
	Images        []BundleImage
	GitCommit     string
	PipelineID    string
	SignatureRef  string
	SBOMRef       string
	ProvenanceRef string
	CreatedAt     time.Time
}

// BundleStore defines the persistence contract for release bundles.
type BundleStore interface {
	Create(ctx context.Context, b *ReleaseBundle) error
	Get(ctx context.Context, id string) (*ReleaseBundle, error)
	GetByDigest(ctx context.Context, alg, value string) (*ReleaseBundle, error)
}

// TrustPolicy defines the verification rules for an environment.
type TrustPolicy struct {
	PolicyVersion  string
	FailClosed     bool
	TrustedIssuers []string
}

// VerificationRecord captures the result of an artifact trust verification.
type VerificationRecord struct {
	ID             string
	ArtifactDigest string
	PolicyVersion  string
	Status         VerificationStatus
	Issuer         string
	Subject        string
	Summary        string
	CreatedAt      time.Time
}

// PreflightCacheKey identifies an artifact preflight result.
type PreflightCacheKey struct {
	OperationID        string
	RoutingVersion     string
	BundleDigest       string
	TrustPolicyVersion string
	SBOMPolicyVersion  string
}

// PreflightRecord stores the serialized result for an idempotent preflight key.
type PreflightRecord struct {
	ID         string
	Key        PreflightCacheKey
	ResultJSON []byte
	CreatedAt  time.Time
}

// ── Inventory domain types (REQ-017) ───────────────────────────────

// InventoryStatus is the lifecycle state of a release in the inventory cache.
type InventoryStatus string

const (
	InventoryActive    InventoryStatus = "active"
	InventoryMissing   InventoryStatus = "missing"
	InventoryOutOfSync InventoryStatus = "out_of_sync" // reserved for future use
)

// ReleaseInventory represents a cached release snapshot in the orchestrator's observation store.
// Unique key: (customer_id, cluster_id, namespace, release_name).
type ReleaseInventory struct {
	CustomerID      string
	ClusterID       string
	Namespace       string
	ReleaseName     string
	Chart           string
	ChartVersion    string
	Revision        int
	Status          string
	ValuesDigest    string
	InventoryStatus InventoryStatus
	LastSyncID      string
	SnapshotVersion int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// InventorySyncLog records the application of a sync snapshot for idempotency.
type InventorySyncLog struct {
	SyncID          string
	CustomerID      string
	ClusterID       string
	IsFullSnapshot  bool
	AcceptedCount   int
	MissingCount    int
	SnapshotVersion int64
	CreatedAt       time.Time
}

// OperationStore defines the persistence contract for operations.
type OperationStore interface {
	Create(ctx context.Context, op *Operation) error
	Get(ctx context.Context, id string) (*Operation, error)
	GetByIdempotencyKey(ctx context.Context, key string) (*Operation, error)
	UpdateStatus(ctx context.Context, id string, status OperationStatus, stateVersion int, lastError string) (*Operation, error)
	HasActiveForDefinition(ctx context.Context, definitionID string) (bool, error)
	HasActiveEmergencyForDefinition(ctx context.Context, definitionID string) (bool, error)
	List(ctx context.Context, definitionID string) ([]*Operation, error)
}

// DefinitionStore defines the persistence contract for release definitions.
type DefinitionStore interface {
	Create(ctx context.Context, def *ReleaseDefinition) error
	Get(ctx context.Context, id string) (*ReleaseDefinition, error)
}

// ValuesStore defines the persistence contract for values revisions.
// For Create, the caller MUST populate Revision via GetNextRevisionNumber
// and Digest via the values package before calling.
type ValuesStore interface {
	Create(ctx context.Context, vr *ValuesRevision) error
	Get(ctx context.Context, id string) (*ValuesRevision, error)
	GetByDigest(ctx context.Context, definitionID, digest string) (*ValuesRevision, error)
	GetLatestApproved(ctx context.Context, definitionID string) (*ValuesRevision, error)
	GetNextRevisionNumber(ctx context.Context, definitionID string) (int, error)
	List(ctx context.Context, definitionID string) ([]*ValuesRevision, error)
	// Update persists status changes with optimistic locking on parent_revision_id.
	// Returns ErrOptimisticLock if expectedParentRev doesn't match the stored value.
	Update(ctx context.Context, vr *ValuesRevision, expectedParentRev string) error
}

// CustomerStore defines the persistence contract for customers.
type CustomerStore interface {
	Create(ctx context.Context, c *Customer) error
	Get(ctx context.Context, id string) (*Customer, error)
	GetBySlug(ctx context.Context, slug string) (*Customer, error)
	Update(ctx context.Context, c *Customer) error
	List(ctx context.Context, includeDisabled bool) ([]*Customer, error)
}

// CustomerEvent is a domain event emitted for customer lifecycle changes.
type CustomerEvent struct {
	ID         string    `json:"id"`
	CustomerID string    `json:"customer_id"`
	EventType  string    `json:"event_type"`
	CreatedAt  time.Time `json:"created_at"`
}

// CustomerEventStore defines the persistence contract for customer events.
type CustomerEventStore interface {
	Create(ctx context.Context, ev *CustomerEvent) error
}

// ClusterStore defines the persistence contract for clusters.
type ClusterStore interface {
	Create(ctx context.Context, c *Cluster) error
	Get(ctx context.Context, id string) (*Cluster, error)
	Update(ctx context.Context, c *Cluster) error
	List(ctx context.Context, customerID string) ([]*Cluster, error)
	ListAll(ctx context.Context) ([]*Cluster, error)
}

// EnrollmentTokenStore defines the persistence contract for enrollment tokens.
type EnrollmentTokenStore interface {
	Create(ctx context.Context, t *EnrollmentToken) error
	GetByToken(ctx context.Context, token string) (*EnrollmentToken, error)
	MarkUsed(ctx context.Context, id, operatorID string) error
	Revoke(ctx context.Context, id string) error
	ListByCustomer(ctx context.Context, customerID string) ([]*EnrollmentToken, error)
	ListByCluster(ctx context.Context, clusterID string) ([]*EnrollmentToken, error)
}

type OperatorStore interface {
	Create(ctx context.Context, op *Operator) error
	Get(ctx context.Context, id string) (*Operator, error)
	GetByCertSerial(ctx context.Context, serial string) (*Operator, error)
	GetByClusterID(ctx context.Context, clusterID string) (*Operator, error)
	GetByName(ctx context.Context, name string) (*Operator, error)
	Update(ctx context.Context, op *Operator) error
	Revoke(ctx context.Context, id string) error
	ListByCustomer(ctx context.Context, customerID string) ([]*Operator, error)
	ListByCluster(ctx context.Context, clusterID string) ([]*Operator, error)
}

// SessionStore defines the persistence contract for operator sessions.
type SessionStore interface {
	Create(ctx context.Context, s *Session) error
	Establish(ctx context.Context, s *Session) error
	Get(ctx context.Context, id string) (*Session, error)
	Heartbeat(ctx context.Context, id string) error
	UpdateStatus(ctx context.Context, id string, status SessionStatus) error
	GetActiveByOperator(ctx context.Context, operatorID string) (*Session, error)
	ListExpiredSuspect(ctx context.Context, suspectAfter time.Duration) ([]*Session, error)
}

// OutboxStore defines the persistence contract for the command outbox.
type OutboxStore interface {
	Create(ctx context.Context, e *OutboxEntry) error
	Get(ctx context.Context, id string) (*OutboxEntry, error)
	GetByCommandID(ctx context.Context, commandID string) (*OutboxEntry, error)
	GetPendingForOperator(ctx context.Context, operatorID string) (*OutboxEntry, error)
	GetDeliveredNotAcked(ctx context.Context, operatorID string) ([]*OutboxEntry, error)
	GetInflightForOperator(ctx context.Context, operatorID string) (*OutboxEntry, error)
	GetNextSequence(ctx context.Context) (int64, error)
	UpdateStatus(ctx context.Context, id string, status CommandStatus, resultJSON string) error
	GetNextPending(ctx context.Context, operatorID string) (*OutboxEntry, error)
}

// UserStore defines the persistence contract for local user accounts (REQ-025).
type UserStore interface {
	Create(ctx context.Context, u *User) error
	Get(ctx context.Context, id string) (*User, error)
	GetByUsername(ctx context.Context, username string) (*User, error)
	Update(ctx context.Context, u *User) error
}

// AuthSessionStore defines the persistence contract for auth sessions (REQ-025).
type AuthSessionStore interface {
	Create(ctx context.Context, s *AuthSession) error
	Get(ctx context.Context, id string) (*AuthSession, error)
	GetByRefreshHash(ctx context.Context, hash string) (*AuthSession, error)
	GetByTokenFamily(ctx context.Context, family string) ([]*AuthSession, error)
	RevokeFamily(ctx context.Context, family string) error
	RevokeByUserID(ctx context.Context, userID string) error
	DeleteExpired(ctx context.Context) (int64, error)
}

// OrganizationStore defines the persistence contract for organizations (REQ-026).
type OrganizationStore interface {
	Create(ctx context.Context, o *Organization) error
	Get(ctx context.Context, id string) (*Organization, error)
	List(ctx context.Context) ([]*Organization, error)
	Update(ctx context.Context, o *Organization) error
}

// OrganizationMemberStore defines the persistence contract for org members (REQ-026).
type OrganizationMemberStore interface {
	Create(ctx context.Context, m *OrganizationMember) error
	Get(ctx context.Context, orgID, userID string) (*OrganizationMember, error)
	ListByOrg(ctx context.Context, orgID string) ([]*OrganizationMember, error)
	ListByUser(ctx context.Context, userID string) ([]*OrganizationMember, error)
	Update(ctx context.Context, m *OrganizationMember) error
	Delete(ctx context.Context, orgID, userID string) error
}

// BindingStore defines the persistence contract for org-customer bindings (REQ-049).
type BindingStore interface {
	Create(ctx context.Context, b *OrgCustomerBinding) error
	Get(ctx context.Context, id string) (*OrgCustomerBinding, error)
	GetByOrgAndCustomer(ctx context.Context, orgID, customerID string) (*OrgCustomerBinding, error)
	ListByOrg(ctx context.Context, orgID string) ([]*OrgCustomerBinding, error)
	Update(ctx context.Context, b *OrgCustomerBinding) error
}

// AuditEventStore defines the persistence contract for audit events (REQ-050).
type AuditEventStore interface {
	Create(ctx context.Context, e *AuditEvent) error
	CreateBatch(ctx context.Context, events []*AuditEvent) error
}

// NotificationStore defines the persistence contract for notification jobs (REQ-031).
type NotificationStore interface {
	Create(ctx context.Context, j *NotificationJob) error
	Get(ctx context.Context, id string) (*NotificationJob, error)
	GetPending(ctx context.Context, now time.Time, limit int) ([]*NotificationJob, error)
	UpdateStatus(ctx context.Context, id string, status NotificationStatus, retryCount int, nextRetryAt *time.Time, lastError string) error
	MarkDeadLetter(ctx context.Context, id string) error
}

// VerificationStore defines the persistence contract for verification records.
type VerificationStore interface {
	Create(ctx context.Context, rec *VerificationRecord) error
	GetByDigestAndPolicy(ctx context.Context, artifactDigest, policyVersion string) (*VerificationRecord, error)
}

// PreflightStore defines the persistence contract for artifact preflight results.
type PreflightStore interface {
	Create(ctx context.Context, rec *PreflightRecord) error
	GetByKey(ctx context.Context, key PreflightCacheKey) (*PreflightRecord, error)
}

// --- Cluster artifact routing domain types (REQ-014) ---

// ArtifactType classifies the kind of artifact routed to a cluster.
type ArtifactType string

const (
	ArtifactImage ArtifactType = "image"
	ArtifactChart ArtifactType = "chart"
)

// Valid returns true if the artifact type is a recognized value.
func (t ArtifactType) Valid() bool {
	return t == ArtifactImage || t == ArtifactChart
}

// ArtifactMode describes how the cluster obtains the artifact.
type ArtifactMode string

const (
	ModeDirect           ArtifactMode = "direct"
	ModePullThroughCache ArtifactMode = "pull_through_cache"
	ModeReplicated       ArtifactMode = "replicated"
)

// Valid returns true if the artifact mode is a recognized value.
func (m ArtifactMode) Valid() bool {
	return m == ModeDirect || m == ModePullThroughCache || m == ModeReplicated
}

// ClusterRoute defines a prefix-based routing rule for a specific artifact type.
type ClusterRoute struct {
	ID           string       `json:"id"`
	ClusterID    string       `json:"cluster_id"`
	ArtifactType ArtifactType `json:"artifact_type"`
	Mode         ArtifactMode `json:"mode"`
	SourcePrefix string       `json:"source_prefix"`
	TargetPrefix string       `json:"target_prefix"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// ClusterRouteStore defines the persistence contract for cluster artifact routes.
type ClusterRouteStore interface {
	Create(ctx context.Context, r *ClusterRoute) error
	Get(ctx context.Context, id string) (*ClusterRoute, error)
	ListByCluster(ctx context.Context, clusterID string) ([]*ClusterRoute, error)
	ListByClusterAndType(ctx context.Context, clusterID string, artifactType ArtifactType) ([]*ClusterRoute, error)
	Update(ctx context.Context, r *ClusterRoute) error
	Delete(ctx context.Context, id string) error
}

// InventoryStore defines the persistence contract for release inventory sync (REQ-017).
type InventoryStore interface {
	// Upsert inserts or updates an inventory row by unique key.
	Upsert(ctx context.Context, item *ReleaseInventory) error

	// ListByCluster returns all inventory rows for a cluster.
	ListByCluster(ctx context.Context, customerID, clusterID string) ([]*ReleaseInventory, error)

	// MarkMissing sets InventoryMissing for all rows in a cluster not present in the given set.
	// Returns the count of rows marked missing.
	MarkMissing(ctx context.Context, customerID, clusterID string, presentKeys []string) (int, error)

	// CreateSyncLog records a sync attempt for idempotency.
	// Returns true if inserted (first time), false if already exists.
	CreateSyncLog(ctx context.Context, log *InventorySyncLog) (bool, error)

	// GetBySyncID checks whether a sync_id has already been applied.
	GetBySyncID(ctx context.Context, syncID string) (*InventorySyncLog, error)
}

// Store is the top-level persistence abstraction.
type Store interface {
	Operations() OperationStore
	Definitions() DefinitionStore
	Values() ValuesStore
	Customers() CustomerStore
	Clusters() ClusterStore
	EnrollmentTokens() EnrollmentTokenStore
	Operators() OperatorStore
	Sessions() SessionStore
	Outbox() OutboxStore
	Users() UserStore
	AuthSessions() AuthSessionStore
	Organizations() OrganizationStore
	OrgMembers() OrganizationMemberStore
	Bindings() BindingStore
	AuditEvents() AuditEventStore
	Notifications() NotificationStore
	Bundles() BundleStore
	Verifications() VerificationStore
	PreflightResults() PreflightStore
	CustomerEvents() CustomerEventStore
	ClusterRoutes() ClusterRouteStore
	Inventories() InventoryStore
	Close() error
}
