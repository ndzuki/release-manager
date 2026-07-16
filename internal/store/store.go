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
	VerificationTrusted              VerificationStatus = "trusted"
	VerificationRejected             VerificationStatus = "rejected"
	VerificationPolicyWarning        VerificationStatus = "policy_warning"
	VerificationSignatureMissing     VerificationStatus = "signature_missing"
	VerificationVerificationUnavailable VerificationStatus = "verification_unavailable"
)

// Sentinel errors for store operations.
var (
	ErrNotFound        = errors.New("store: not found")
	ErrOptimisticLock  = errors.New("store: optimistic lock conflict")
	ErrDuplicateKey    = errors.New("store: duplicate key")
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
	ValuesStatusDraft   ValuesStatus = "draft"
	ValuesStatusApproved ValuesStatus = "approved"
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
	ID               string           `json:"id"`
	Name             string           `json:"name"`
	CustomerID       string           `json:"customer_id"`
	ClusterID        string           `json:"cluster_id"`
	Namespace        string           `json:"namespace"`
	ReleaseName      string           `json:"release_name"`
	ChartName        string           `json:"chart_name"`
	Status           DefinitionStatus `json:"status"`
	OptimisticVersion int             `json:"optimistic_version"`
	CreatedBy        string           `json:"created_by"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
}

// ValuesRevision stores the desired configuration for a release target.
type ValuesRevision struct {
	ID                  string       `json:"id"`
	ReleaseDefinitionID string       `json:"release_definition_id"`
	Revision            int          `json:"revision"`
	Status              ValuesStatus `json:"status"`
	Values              []byte       `json:"values"`
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
	OperatorActive    OperatorStatus = "active"
	OperatorSuperseded OperatorStatus = "superseded"
	OperatorRevoked   OperatorStatus = "revoked"
)

// SessionStatus tracks the operator connection lifecycle.
type SessionStatus string

const (
	SessionOnline   SessionStatus = "online"
	SessionSuspect  SessionStatus = "suspect"
	SessionOffline  SessionStatus = "offline"
)

// CommandStatus is the outbox delivery and execution state.
type CommandStatus string

const (
	CommandPending     CommandStatus = "pending"
	CommandDelivered   CommandStatus = "delivered"
	CommandPersisted   CommandStatus = "persisted"
	CommandRunning     CommandStatus = "running"
	CommandSucceeded   CommandStatus = "succeeded"
	CommandFailed      CommandStatus = "failed"
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
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	CustomerID   string        `json:"customer_id"`
	KubeconfigRef string       `json:"kubeconfig_ref"`
	Status       ClusterStatus `json:"status"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

// EnrollmentToken is a single-use token for operator registration.
type EnrollmentToken struct {
	ID           string     `json:"id"`
	CustomerID   string     `json:"customer_id"`
	ClusterID    string     `json:"cluster_id"`
	Token        string     `json:"token"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	Used         bool       `json:"used"`
	UsedAt       *time.Time `json:"used_at,omitempty"`
	OperatorID   string     `json:"operator_id,omitempty"`
}

// Operator represents a registered operator agent in a cluster.
type Operator struct {
	ID              string         `json:"id"`
	CustomerID      string         `json:"customer_id"`
	ClusterID       string         `json:"cluster_id"`
	CertSerial      string         `json:"cert_serial"`
	Status          OperatorStatus `json:"status"`
	SupersededBy    string         `json:"superseded_by,omitempty"`
	RevokedAt       *time.Time     `json:"revoked_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// Session tracks a live operator connection.
type Session struct {
	ID             string        `json:"id"`
	OperatorID     string        `json:"operator_id"`
	Status         SessionStatus `json:"status"`
	StartedAt      time.Time     `json:"started_at"`
	LastHeartbeat  time.Time     `json:"last_heartbeat"`
	ExpiresAt      time.Time     `json:"expires_at"`
}

// OutboxEntry holds a command pending delivery in the outbox.
type OutboxEntry struct {
	ID            string        `json:"id"`
	OperationID   string        `json:"operation_id"`
	OperatorID    string        `json:"operator_id"`
	Payload       []byte        `json:"payload"`
	Status        CommandStatus `json:"status"`
	MaxInFlight   int           `json:"max_inflight"`
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
	ID          string
	OperationID string
	Channel     NotificationChannel
	Recipient   string
	Status      NotificationStatus
	RetryCount  int
	MaxRetries  int
	NextRetryAt *time.Time
	LastError   string
	DeadLetterAt *time.Time
	Metadata    map[string]string
	CreatedAt   time.Time
	UpdatedAt   time.Time
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
	EmergencyRequirePromotion    EmergencyConvergence = "require_promotion"
	EmergencyRevertOnNextReconcile EmergencyConvergence = "revert_on_next_reconcile"
)

// EmergencyPayload carries the typed emergency change request data.
type EmergencyPayload struct {
	Action      EmergencyAction
	Payload     string // JSON-encoded action-specific parameters
	Reason      string
	Convergence EmergencyConvergence
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
	Update(ctx context.Context, def *ReleaseDefinition) error
	List(ctx context.Context) ([]*ReleaseDefinition, error)
}

// ValuesStore defines the persistence contract for values revisions.
type ValuesStore interface {
	Create(ctx context.Context, vr *ValuesRevision) error
	Get(ctx context.Context, id string) (*ValuesRevision, error)
	GetLatestApproved(ctx context.Context, definitionID string) (*ValuesRevision, error)
	List(ctx context.Context, definitionID string) ([]*ValuesRevision, error)
}


// CustomerStore defines the persistence contract for customers.
type CustomerStore interface {
	Create(ctx context.Context, c *Customer) error
	Get(ctx context.Context, id string) (*Customer, error)
	GetBySlug(ctx context.Context, slug string) (*Customer, error)
	Update(ctx context.Context, c *Customer) error
	List(ctx context.Context) ([]*Customer, error)
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
}

// OperatorStore defines the persistence contract for operators.
type OperatorStore interface {
	Create(ctx context.Context, op *Operator) error
	Get(ctx context.Context, id string) (*Operator, error)
	GetByCertSerial(ctx context.Context, serial string) (*Operator, error)
	Update(ctx context.Context, op *Operator) error
}

// SessionStore defines the persistence contract for operator sessions.
type SessionStore interface {
	Create(ctx context.Context, s *Session) error
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
	GetPendingForOperator(ctx context.Context, operatorID string) (*OutboxEntry, error)
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
	Verifications() VerificationStore
	Close() error
}
