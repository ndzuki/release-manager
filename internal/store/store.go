// Package store defines the persistence layer interfaces and domain types
// for the release-manager core pipeline.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
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
	ErrNotFound                  = errors.New("store: not found")
	ErrOptimisticLock            = errors.New("store: optimistic lock conflict")
	ErrDuplicateKey              = errors.New("store: duplicate key")
	ErrUnavailable               = errors.New("store: unavailable")
	ErrReleaseBusy               = errors.New("store: release busy")
	ErrInvalidCursor             = errors.New("store: invalid cursor")
	ErrBindingRevoked            = errors.New("store: binding revoked")
	ErrApprovalPending           = errors.New("store: another approval is pending")
	ErrIdempotencyConflict       = errors.New("store: idempotency conflict")
	ErrInvalidState              = errors.New("store: invalid values revision state")
	ErrDefinitionOwnerUnresolved = errors.New("store: definition owner unresolved")
	ErrNotAuthorized             = errors.New("store: approval command not authorized")
	ErrEmergencyConflict         = errors.New("store: conflicting emergency target")
	ErrAuthorizationStale        = errors.New("store: authorization source version changed")
)

// StateVersionConflictError reports the current revision version after a failed CAS.
type StateVersionConflictError struct {
	Expected int64
	Current  int64
}

func (e *StateVersionConflictError) Error() string {
	return "store: values revision state version conflict"
}

func (e *StateVersionConflictError) Unwrap() error { return ErrOptimisticLock }

// OperationStateVersionConflictError reports the current operation version after a failed CAS.
type OperationStateVersionConflictError struct {
	Expected int
	Current  int
}

func (e *OperationStateVersionConflictError) Error() string {
	return "store: operation state version conflict"
}

func (e *OperationStateVersionConflictError) Unwrap() error { return ErrOptimisticLock }

// InvalidValuesStateError reports a state-machine precondition failure.
type InvalidValuesStateError struct {
	Actual   ValuesStatus
	Expected ValuesStatus
}

func (e *InvalidValuesStateError) Error() string { return "store: invalid values revision state" }

func (e *InvalidValuesStateError) Unwrap() error { return ErrInvalidState }

// ApprovalPendingError reports the definition whose pending slot is occupied.
type ApprovalPendingError struct {
	DefinitionID string
}

func (e *ApprovalPendingError) Error() string { return "store: another approval is pending" }

func (e *ApprovalPendingError) Unwrap() error { return ErrApprovalPending }
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

// CanTransitionTo reports whether an operation status may advance directly to target.
//
//nolint:gocyclo // Exhaustive legal-transition table; simpler than a two-layered map.
func (s OperationStatus) CanTransitionTo(target OperationStatus) bool {
	switch s {
	case StatusPending:
		return target == StatusPreflight || target == StatusQueued || target == StatusCancelled || target == StatusTimeout
	case StatusPreflight:
		return target == StatusQueued || target == StatusFailed || target == StatusCancelled || target == StatusTimeout
	case StatusQueued:
		return target == StatusRunning || target == StatusCancelled || target == StatusTimeout
	case StatusRunning:
		return target == StatusSucceeded || target == StatusFailed || target == StatusCancelling || target == StatusTimeout
	case StatusCancelling:
		return target == StatusCancelled || target == StatusFailed || target == StatusTimeout
	default:
		return false
	}
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
	ValuesStatusDraft      ValuesStatus = "draft"
	ValuesStatusApproved   ValuesStatus = "approved"
	ValuesStatusRejected   ValuesStatus = "rejected"
	ValuesStatusSuperseded ValuesStatus = "superseded"
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
	TargetRevision      int             `json:"target_revision,omitempty"`
	ValuesPatch         []byte          `json:"values_patch,omitempty"`
	Actor               ActorContext    `json:"actor"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
	Deadline            *time.Time      `json:"deadline,omitempty"`
	TerminalAt          *time.Time      `json:"terminal_at,omitempty"`
	LastError           string          `json:"last_error,omitempty"`
}

// OperationCancelCommand contains the trusted authorization and idempotency snapshot for cancellation.
type OperationCancelCommand struct {
	OperationID          string
	ExpectedStateVersion int
	TargetStatus         OperationStatus
	ActorUserID          string
	Reason               string
	RequestID            string
	IdempotencyScope     string
	IdempotencyKeyHash   string
	RequestHash          string
	DeliveryStatus       OperationDeliveryStatus
}

// OperationCancelReplayQuery identifies one authorized cancellation intent.
type OperationCancelReplayQuery struct {
	OperationID        string
	ActorUserID        string
	IdempotencyKeyHash string
	RequestHash        string
}

// OperationCancelResult is the committed or replayed cancellation transition.
type OperationCancelResult struct {
	Operation *Operation `json:"operation"`
	RequestID string     `json:"request_id"`
	Replayed  bool       `json:"-"`
}

// OperationDeliveryStatus describes whether an EMERGENCY command crossed the delivery boundary.
type OperationDeliveryStatus string

const (
	DeliveryUndelivered OperationDeliveryStatus = "undelivered"
	DeliveryDelivered   OperationDeliveryStatus = "delivered"
	DeliveryUnknown     OperationDeliveryStatus = "unknown"
)

// EmergencyEffectStatus is the authoritative cluster effect of an EMERGENCY operation.
type EmergencyEffectStatus string

const (
	EmergencyEffectUnknown    EmergencyEffectStatus = "UNKNOWN"
	EmergencyEffectApplied    EmergencyEffectStatus = "APPLIED"
	EmergencyEffectNotApplied EmergencyEffectStatus = "NOT_APPLIED"
)

// OperationTimelineEntryKind identifies sanitized Operation timeline payloads.
type OperationTimelineEntryKind string

const (
	TimelineEntryStateTransition         OperationTimelineEntryKind = "STATE_TRANSITION"
	TimelineEntryEmergencyEffectResolved OperationTimelineEntryKind = "EMERGENCY_EFFECT_RESOLVED"
)

// OperationTimelineEntry records one ordered, sanitized observation for an Operation.
type OperationTimelineEntry struct {
	ID                    string
	OperationID           string
	Sequence              int64
	OperationStateVersion int
	Kind                  string
	Data                  json.RawMessage
	CreatedAt             time.Time
}

// StateTransitionTimelineData is the sanitized payload for a state transition.
type StateTransitionTimelineData struct {
	RequestID string `json:"request_id,omitempty"`
	FromState string `json:"from_state"`
	ToState   string `json:"to_state"`
	ErrorCode string `json:"error_code,omitempty"`
}

// EmergencyEffectTimelineData is the sanitized payload for a late effect resolution.
type EmergencyEffectTimelineData struct {
	RequestID  string `json:"request_id,omitempty"`
	EffectFrom string `json:"effect_from"`
	EffectTo   string `json:"effect_to"`
}

// TimelineSnapshot is an Operation and its committed Timeline sequence boundary.
type TimelineSnapshot struct {
	Operation            *Operation
	SnapshotSequence     int64
	RetainedFromSequence int64
}

// TimelineStore provides ordered persistence and snapshot boundaries for Operation watches.
type TimelineStore interface {
	Append(ctx context.Context, entry *OperationTimelineEntry) (*OperationTimelineEntry, error)
	List(ctx context.Context, operationID string, afterSequence, throughSequence int64) ([]*OperationTimelineEntry, error)
	LatestSequence(ctx context.Context, operationID string) (int64, error)
	Snapshot(ctx context.Context, operationID string) (*TimelineSnapshot, error)
}

// ReleaseDefinition represents a Helm release target configuration.
type ReleaseDefinition struct {
	ID                     string                  `json:"id"`
	Name                   string                  `json:"name"`
	CustomerID             string                  `json:"customer_id"`
	ClusterID              string                  `json:"cluster_id"`
	Namespace              string                  `json:"namespace"`
	ReleaseName            string                  `json:"release_name"`
	ChartName              string                  `json:"chart_name"`
	Status                 DefinitionStatus        `json:"status"`
	OptimisticVersion      int                     `json:"optimistic_version"`
	CurrentBundleID        *string                 `json:"current_bundle_id,omitempty"`
	CreatedBy              string                  `json:"created_by"`
	OwnerOrganizationID    *string                 `json:"owner_organization_id,omitempty"`
	ApprovedRevisionID     *string                 `json:"approved_revision_id,omitempty"`
	HPAManaged             bool                    `json:"hpa_managed"`
	MaxEmergencyReplicas   int32                   `json:"max_emergency_replicas"`
	ApprovedAnnotationKeys []ApprovedAnnotationKey `json:"approved_annotation_keys"`
	PromotionMappings      []PromotionMapping      `json:"promotion_mappings"`
	CreatedAt              time.Time               `json:"created_at"`
	UpdatedAt              time.Time               `json:"updated_at"`
}

// ValuesRevision stores the desired configuration for a release target.
type ValuesRevision struct {
	ID                  string       `json:"id"`
	ReleaseDefinitionID string       `json:"release_definition_id"`
	Revision            int          `json:"revision"`
	Version             int          `json:"version"`
	Status              ValuesStatus `json:"status"`
	Values              []byte       `json:"values"`
	Digest              string       `json:"digest"`
	ParentRevisionID    string       `json:"parent_revision_id"`
	SecretRefs          []byte       `json:"secret_refs,omitempty"`
	CreatedBy           string       `json:"created_by"`
	ApprovedBy          string       `json:"approved_by,omitempty"`
	ApprovedAt          *time.Time   `json:"approved_at,omitempty"`
	RejectedBy          string       `json:"rejected_by,omitempty"`
	RejectedAt          *time.Time   `json:"rejected_at,omitempty"`
	RejectionReason     string       `json:"rejection_reason,omitempty"`
	CreatedAt           time.Time    `json:"created_at"`
	UpdatedAt           time.Time    `json:"updated_at"`
}

// ValuesDecisionAction identifies a durable approval workflow transition.
type ValuesDecisionAction string

const (
	ValuesDecisionSubmitted ValuesDecisionAction = "submitted"
	ValuesDecisionApproved  ValuesDecisionAction = "approved"
	ValuesDecisionRejected  ValuesDecisionAction = "rejected"
)

// ValuesRevisionDecision is an immutable state transition record.
type ValuesRevisionDecision struct {
	ID                  string               `json:"id"`
	RevisionID          string               `json:"revision_id"`
	ReleaseDefinitionID string               `json:"release_definition_id"`
	Action              ValuesDecisionAction `json:"action"`
	FromState           ValuesStatus         `json:"from_state"`
	ToState             ValuesStatus         `json:"to_state"`
	ActorUserID         string               `json:"actor_user_id"`
	ActorOrgID          string               `json:"actor_org_id"`
	ActorRole           Role                 `json:"actor_role"`
	Comment             *string              `json:"comment,omitempty"`
	Reason              string               `json:"reason,omitempty"`
	RequestID           string               `json:"request_id,omitempty"`
	IdempotencyKeyHash  string               `json:"idempotency_key_hash,omitempty"`
	CreatedAt           time.Time            `json:"created_at"`
}

// ApprovalOutboxEntry is a durable audit or notification event.
type ApprovalOutboxEntry struct {
	ID          string     `json:"id"`
	EventType   string     `json:"event_type"`
	PayloadJSON []byte     `json:"payload_json"`
	CreatedAt   time.Time  `json:"created_at"`
	Delivered   bool       `json:"delivered"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

// ValuesApprovalCommand contains the trusted authorization and idempotency snapshot.
type ValuesApprovalCommand struct {
	RevisionID                   string
	ExpectedStateVersion         int64
	ExpectedAuthorizationVersion uint64
	ActorUserID                  string
	ActorOrgID                   string
	ActorRole                    Role
	Authorized                   bool
	Comment                      *string
	Reason                       string
	RequestID                    string
	IdempotencyScope             string
	IdempotencyKeyHash           string
	RequestHash                  string
}

// ValuesApprovalResult is the committed or replayed transition result.
type ValuesApprovalResult struct {
	Revision              *ValuesRevision
	PreviousState         ValuesStatus
	NewState              ValuesStatus
	DecidedAt             time.Time
	SupersededRevisionIDs []string
	Replayed              bool
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
	Version       int64         `json:"version"`
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
	Status              SessionStatus     `json:"status"`
	InstanceID          string            `json:"instance_id"`
	Version             string            `json:"version"`
	Capabilities        map[string]string `json:"capabilities"`
	ActiveConfigVersion string            `json:"active_config_version"`
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
	UserPending  UserStatus = "pending"
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
	Provider     string
	Subject      string
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

// AuthorizationAction is a governance capability enforced by REQ-027.
type AuthorizationAction string

const (
	AuthorizationExecuteEmergency AuthorizationAction = "release.emergency.execute"
	AuthorizationResolveEmergency AuthorizationAction = "release.emergency.resolve"
	AuthorizationCreateValues     AuthorizationAction = "release.values.create"
	AuthorizationApproveValues    AuthorizationAction = "release.values.approve"
)

// Valid reports whether the action is part of the fixed authorization contract.
func (a AuthorizationAction) Valid() bool {
	switch a {
	case AuthorizationExecuteEmergency, AuthorizationResolveEmergency,
		AuthorizationCreateValues, AuthorizationApproveValues:
		return true
	}
	return false
}

// CapabilityGrant is an explicit subject capability override.
type CapabilityGrant struct {
	OrganizationID string
	Subject        string
	Action         AuthorizationAction
	GrantedBy      string
	Revoked        bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CasbinRule is one durable p/g rule consumed by the authorization enforcer.
type CasbinRule struct {
	PType string
	V0    string
	V1    string
	V2    string
	V3    string
	V4    string
	V5    string
}

// AuthorizationCheckpoint is the persisted consumer progress for one organization/customer scope.
type AuthorizationCheckpoint struct {
	OrganizationID string
	CustomerID     string
	SourceVersion  uint64
	PolicyVersion  uint64
	Fresh          bool
	UpdatedAt      time.Time
}

// AuthorizationSnapshot is the durable authorization state restored by Auth and Orchestrator.
type AuthorizationSnapshot struct {
	SourceVersion uint64
	PolicyVersion uint64
	Grants        []CapabilityGrant
	Rules         []CasbinRule
	Checkpoints   []AuthorizationCheckpoint
}

// AuthorizationMutation identifies one source-version advancing change.
type AuthorizationMutation string

const (
	AuthorizationMembershipChanged AuthorizationMutation = "membership_changed"
	AuthorizationBindingChanged    AuthorizationMutation = "binding_changed"
	AuthorizationGrantChanged      AuthorizationMutation = "grant_changed"
	AuthorizationPolicyChanged     AuthorizationMutation = "policy_changed"
)

// AuthorizationApplyCommand atomically replaces durable rules and optional capability grants.
type AuthorizationApplyCommand struct {
	ExpectedSourceVersion uint64
	ExpectedPolicyVersion uint64
	Mutation              AuthorizationMutation
	Grants                []CapabilityGrant
	Rules                 []CasbinRule
}

// Validate enforces the mutation-to-payload contract that protects version fences.
func (c AuthorizationApplyCommand) Validate() error {
	switch c.Mutation {
	case AuthorizationMembershipChanged, AuthorizationBindingChanged:
		if c.Grants != nil || c.Rules != nil {
			return errors.New("store: membership and binding mutations cannot replace authorization state")
		}
	case AuthorizationGrantChanged:
		if c.Grants == nil || c.Rules == nil {
			return errors.New("store: grant mutation requires grants and rules")
		}
	case AuthorizationPolicyChanged:
		if c.Grants != nil || c.Rules == nil {
			return errors.New("store: policy mutation requires rules only")
		}
	default:
		return errors.New("store: invalid authorization mutation")
	}
	return nil
}

// AuthorizationStore owns the version guards, grants, rules, and consumer checkpoints.
type AuthorizationStore interface {
	Load(ctx context.Context) (*AuthorizationSnapshot, error)
	Apply(ctx context.Context, command AuthorizationApplyCommand) (*AuthorizationSnapshot, error)
	GetCheckpoint(ctx context.Context, organizationID, customerID string) (*AuthorizationCheckpoint, error)
	SaveCheckpoint(ctx context.Context, checkpoint AuthorizationCheckpoint) error
}

// --- Audit & Notification domain types (REQ-050, REQ-031) ---

// AuditActorKind classifies the principal that performed an action.
type AuditActorKind string

const (
	AuditActorAnonymous AuditActorKind = "anonymous"
	AuditActorUser      AuditActorKind = "user"
	AuditActorService   AuditActorKind = "service"
	AuditActorAPIKey    AuditActorKind = "api_key"
	AuditActorSystem    AuditActorKind = "system"
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

// AuditEventFilter narrows audit event queries by optional criteria.
type AuditEventFilter struct {
	OrganizationID string
	ResourceType   string
	ResourceID     string
	ActorID        string
	Action         string
	Status         string
	Since          *time.Time
	Until          *time.Time
}

// AuditEventPage is a cursor-based page of audit events.
type AuditEventPage struct {
	Events     []*AuditEvent
	HasMore    bool
	NextCursor string
}

// AuditExport represents a requested audit export job.
type AuditExport struct {
	ID             string
	OrganizationID string
	Since          time.Time
	Until          time.Time
	Status         string
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
	Attempts     int
	RetryCount   int
	MaxRetries   int
	ErrorCode    string
	NextRetryAt  *time.Time
	LastError    string
	SentAt       *time.Time
	DeadLetterAt *time.Time
	Metadata     map[string]string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// --- Emergency change domain types (REQ-032) ---
// EmergencyAction is a typed operation whitelisted for emergency changes.
type EmergencyAction string

const (
	EmergencySetContainerImage      EmergencyAction = "set_container_image"
	EmergencySetReplicas            EmergencyAction = "set_replicas"
	EmergencySetApprovedAnnotations EmergencyAction = "set_approved_annotation"
)

// Valid returns true if the emergency action is a recognized value.
func (a EmergencyAction) Valid() bool {
	switch a {
	case EmergencySetContainerImage, EmergencySetReplicas, EmergencySetApprovedAnnotations:
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

type ApprovedAnnotationKey struct {
	Key                 string `json:"key"`
	Scope               string `json:"scope"`
	PromotionValuesPath string `json:"promotion_values_path,omitempty"`
}

type PromotionMapping struct {
	WorkloadKind string `json:"workload_kind"`
	WorkloadName string `json:"workload_name"`
	Container    string `json:"container,omitempty"`
	Field        string `json:"field"`
	ValuesPath   string `json:"values_path"`
}

type EmergencyIntent struct {
	ID                  string
	ReleaseDefinitionID string
	OperationID         string
	CommandID           string
	Action              EmergencyAction
	WorkloadKind        string
	WorkloadName        string
	WorkloadNamespace   string
	WorkloadUID         string
	Container           *string
	ArtifactID          *string
	ImageReference      *string
	TargetReplicas      *int32
	AnnotationScope     *string
	AnnotationEntries   json.RawMessage
	Convergence         EmergencyConvergence
	PromotionPaths      json.RawMessage
	BeforeSnapshot      json.RawMessage
	AfterSnapshot       json.RawMessage
	DeliveryStatus      string
	EffectStatus        EmergencyEffectStatus
	LastDeliveryAt      *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ResolveEmergencyEffectCommand carries one authoritative late EMERGENCY result.
type ResolveEmergencyEffectCommand struct {
	OperationID          string
	ExpectedStateVersion int
	EffectStatus         EmergencyEffectStatus
	BeforeSnapshot       json.RawMessage
	AfterSnapshot        json.RawMessage
	RequestID            string
}

// ResolveEmergencyEffectResult reports whether a late result changed the aggregate.
type ResolveEmergencyEffectResult struct {
	Operation *Operation
	Intent    *EmergencyIntent
	Timeline  *OperationTimelineEntry
	Resolved  bool
}

type ConvergenceTask struct {
	ID                   string
	OperationID          string
	ReleaseDefinitionID  string
	Action               EmergencyAction
	TargetSummary        string
	Reason               string
	PromotionPaths       json.RawMessage
	Status               string
	ActiveRevisionID     *string
	ActiveRevisionStatus *string
	LastRejectionReason  *string
	SubmittedAt          time.Time
	ConvergedAt          *time.Time
	CreatedAt            time.Time
}

type EmergencyCreateCommand struct {
	Operation                    *Operation
	Intent                       *EmergencyIntent
	ConvergenceTask              *ConvergenceTask
	ExpectedAuthorizationVersion uint64
	IdempotencyScope             string
	IdempotencyKeyHash           string
	RequestHash                  string
	IdempotencyExpiresAt         time.Time
}

type EmergencyCreateResult struct {
	Operation       *Operation
	Intent          *EmergencyIntent
	ConvergenceTask *ConvergenceTask
	Replayed        bool
}

type EmergencyIntentStore interface {
	CreateIfAvailable(ctx context.Context, command EmergencyCreateCommand) (*EmergencyCreateResult, error)
	GetReplay(ctx context.Context, scope, keyHash, requestHash string) (*EmergencyCreateResult, error)
	GetByOperationID(ctx context.Context, operationID string) (*EmergencyIntent, error)
	GetByCommandID(ctx context.Context, commandID string) (*EmergencyIntent, error)
	GetActiveLocksForDefinition(ctx context.Context, definitionID string) ([]*EmergencyIntent, error)
	ListPendingDeliveryByDefinition(ctx context.Context, definitionID string) ([]*EmergencyIntent, error)
	UpdateDeliveryStatus(ctx context.Context, id, status string) error
	Finish(ctx context.Context, intentID, operationID string, expectedStateVersion int, status OperationStatus, effectStatus EmergencyEffectStatus, lastError string, beforeSnapshot, afterSnapshot json.RawMessage) (*Operation, error)
	ResolveEmergencyEffect(ctx context.Context, command ResolveEmergencyEffectCommand) (*ResolveEmergencyEffectResult, error)
}

type ConvergenceTaskStore interface {
	Create(ctx context.Context, task *ConvergenceTask) error
	ListByDefinition(ctx context.Context, definitionID, statusFilter string) ([]*ConvergenceTask, error)
	GetByOperationID(ctx context.Context, operationID string) (*ConvergenceTask, error)
	HasPendingPromotionForDefinition(ctx context.Context, definitionID string) (bool, error)
	HasPendingPromotionPath(ctx context.Context, definitionID string, promotionPaths []string) (bool, error)
	MarkConverged(ctx context.Context, id, revisionID string) error
	BindRevision(ctx context.Context, id, revisionID, revisionStatus string) error
}

// --- Trust root domain types (REQ-043) ---

// TrustRootState is the lifecycle state of a trust root.
type TrustRootState string

const (
	TrustRootPending TrustRootState = "pending"
	TrustRootActive  TrustRootState = "active"
	TrustRootGrace   TrustRootState = "grace"
	TrustRootRetired TrustRootState = "retired"
	TrustRootRevoked TrustRootState = "revoked"
)

func (s TrustRootState) Valid() bool {
	switch s {
	case TrustRootPending, TrustRootActive, TrustRootGrace, TrustRootRetired, TrustRootRevoked:
		return true
	}
	return false
}

type TrustRoot struct {
	ID             string
	Environment    string
	KeyID          string
	PublicKeyPEM   string
	Issuer         string
	SubjectPattern string
	State          TrustRootState
	ValidFrom      time.Time
	GraceUntil     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	RevokedAt      *time.Time
}

type TrustPolicyMeta struct {
	Environment     string
	Version         int64
	RevocationEpoch int64
}

type TrustRootStore interface {
	Create(ctx context.Context, r *TrustRoot) error
	Get(ctx context.Context, id string) (*TrustRoot, error)
	ListByEnvironment(ctx context.Context, env string) ([]*TrustRoot, error)
	GetActiveByEnvironment(ctx context.Context, env string, at time.Time) ([]*TrustRoot, error)
	Update(ctx context.Context, r *TrustRoot) error
	GetPolicy(ctx context.Context, env string) (*TrustPolicyMeta, error)
	BumpPolicy(ctx context.Context, env string) (version int64, epoch int64, err error)
	BumpRevocationEpoch(ctx context.Context, env string) (int64, error)
}

// --- Bundle domain types (REQ-011) ---

// BundleStatus is the lifecycle state of a ReleaseBundle.
type BundleStatus string

const (
	BundleReceived  BundleStatus = "received"
	BundleValidated BundleStatus = "validated"
	BundleRejected  BundleStatus = "rejected"
	BundleArchived  BundleStatus = "archived"
)

// Valid returns true if the status is a recognized value.
func (s BundleStatus) Valid() bool {
	switch s {
	case BundleReceived, BundleValidated, BundleRejected, BundleArchived:
		return true
	default:
		return false
	}
}

// ImageValueKind describes which Helm value a binding replaces.
type ImageValueKind string

const (
	ImageValueFullReference ImageValueKind = "FULL_REFERENCE"
	ImageValueRepository    ImageValueKind = "REPOSITORY"
	ImageValueTag           ImageValueKind = "TAG"
	ImageValueDigest        ImageValueKind = "DIGEST"
)

// Valid returns true if the value kind is recognized.
func (k ImageValueKind) Valid() bool {
	switch k {
	case ImageValueFullReference, ImageValueRepository, ImageValueTag, ImageValueDigest:
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
	ValueKind  ImageValueKind
}

// ArtifactReference identifies immutable evidence stored outside the service.
type ArtifactReference struct {
	Ref    string
	Digest string
}

// ReleaseBundle represents an immutable release artifact bundle.
type ReleaseBundle struct {
	ID                 string
	Name               string
	DigestAlg          string
	DigestValue        string
	Status             BundleStatus
	ChartRef           string
	ChartVersion       string
	ChartDigest        string
	Images             []BundleImage
	GitCommit          string
	PipelineID         string
	SignatureRef       string
	SignatureDigest    string
	SBOMRef            string
	SBOMDigest         string
	ProvenanceRef      string
	ProvenanceDigest   string
	ArchivedAt         *time.Time
	ArchivedFromStatus *BundleStatus
	CreatedAt          time.Time
}

// BundleListFilter narrows bundle queries and binds opaque pagination tokens.
type BundleListFilter struct {
	ReleaseDefinitionID string
	Statuses            []BundleStatus
	ChartName           string
	PageSize            int
	PageToken           string
}

// BundlePage is one stable page of ReleaseBundles.
type BundlePage struct {
	Bundles       []*ReleaseBundle
	NextPageToken string
	TotalSize     int32
}

// BundleStore defines the persistence contract for release bundles.
type BundleStore interface {
	Create(ctx context.Context, b *ReleaseBundle) error
	CreateTx(tx *gorm.DB, b *ReleaseBundle) error
	Get(ctx context.Context, id string) (*ReleaseBundle, error)
	GetByDigest(ctx context.Context, alg, value string) (*ReleaseBundle, error)
	GetByAlias(ctx context.Context, alias string) (*ReleaseBundle, error)
	List(ctx context.Context, filter BundleListFilter) (*BundlePage, error)
	UpdateStatusTx(tx *gorm.DB, id string, from, to BundleStatus, validationErr string) error
	ListForArchive(ctx context.Context, retentionDays int, terminalStates []OperationStatus) ([]string, error)
	Archive(ctx context.Context, ids []string) (int64, error)
	Unarchive(ctx context.Context, id string) error
	DeleteBefore(ctx context.Context, cutoff time.Time) (int64, error)
}

// TrustPolicy defines the verification rules for an environment.
type TrustPolicy struct {
	PolicyVersion  string
	FailClosed     bool
	TrustedIssuers []string
}

// VerificationRecord captures the result of an artifact trust verification.
type VerificationRecord struct {
	ID              string
	ArtifactDigest  string
	PolicyVersion   string
	Status          VerificationStatus
	RootID          string
	KeyID           string
	RevocationEpoch int64
	Issuer          string
	Subject         string
	Summary         string
	CreatedAt       time.Time
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

// ScanResultRecord is the serializable domain type for a vulnerability scan result.
type ScanResultRecord struct {
	ID             string
	ArtifactDigest string
	SBOMRef        string
	Scanner        string
	ResultVersion  string
	SeverityJSON   []byte
	FindingsJSON   []byte
	ScannedAt      time.Time
	CreatedAt      time.Time
}

// VulnerabilityExceptionRecord is the serializable domain type for a time-bounded exception.
type VulnerabilityExceptionRecord struct {
	ID             string
	FindingID      string
	ArtifactDigest string
	Actor          string
	Reason         string
	ExpiresAt      time.Time
	CreatedAt      time.Time
}

// ── Inventory domain types (REQ-017) ───────────────────────────────

// InventoryStatus is the lifecycle state of a release in the inventory cache.
type InventoryStatus string

const (
	InventoryActive    InventoryStatus = "active"
	InventoryMissing   InventoryStatus = "missing"
	InventoryOutOfSync InventoryStatus = "out_of_sync" // reserved for future use
)

// OperationExecutionResult stores the typed terminal payload for one operation.
type OperationExecutionResult struct {
	OperationID   string
	ResultType    string
	ResultPayload []byte
	CreatedAt     time.Time
}

// RolloutTracking records asynchronous rollout observation after a successful upgrade.
type RolloutTracking struct {
	OperationID   string
	Status        string
	ResourceCount int
	ReadyCount    int
	FailedCount   int
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// UpgradeTerminalInput is applied atomically when a typed operator result arrives.
type UpgradeTerminalInput struct {
	OperationID                   string
	ExpectedStateVersion          int
	Status                        OperationStatus
	LastError                     string
	ResultPayload                 []byte
	ReleaseDefinitionID           string
	CustomerID                    string
	ClusterID                     string
	UpdateInventory               bool
	Revision                      int
	ObservedBundleDigest          string
	ObservedChartDigest           string
	ObservedEffectiveValuesDigest string
	ObservedManifestDigest        string
	LiveStatus                    string
	InventoryStatus               InventoryStatus
	ResourceCount                 int
}

// ReleaseInventory represents a cached release snapshot in the orchestrator's observation store.
// Unique key: (customer_id, cluster_id, namespace, release_name).
type ReleaseInventory struct {
	ReleaseDefinitionID           string
	CustomerID                    string
	ClusterID                     string
	Namespace                     string
	ReleaseName                   string
	Chart                         string
	ChartVersion                  string
	Revision                      int
	Status                        string
	ValuesDigest                  string
	ObservedBundleDigest          string
	ObservedChartDigest           string
	ObservedEffectiveValuesDigest string
	ObservedManifestDigest        string
	LiveStatus                    string
	LastOperationID               string
	InventoryStatus               InventoryStatus
	LastSyncID                    string
	SnapshotVersion               int64
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
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

type InventorySyncRequestStatus string

const (
	InventorySyncPending   InventorySyncRequestStatus = "pending"
	InventorySyncRunning   InventorySyncRequestStatus = "running"
	InventorySyncSucceeded InventorySyncRequestStatus = "succeeded"
	InventorySyncFailed    InventorySyncRequestStatus = "failed"
)

func (s InventorySyncRequestStatus) IsTerminal() bool {
	return s == InventorySyncSucceeded || s == InventorySyncFailed
}

type InventorySyncRequest struct {
	ID         string
	CustomerID string
	ClusterID  string
	OperatorID string
	CommandID  string
	Status     InventorySyncRequestStatus
	LastError  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type InventorySyncRequestStore interface {
	CreateIfAvailable(ctx context.Context, request *InventorySyncRequest, outbox *OutboxEntry) (*InventorySyncRequest, bool, error)
	Get(ctx context.Context, id string) (*InventorySyncRequest, error)
	GetActiveByCluster(ctx context.Context, customerID, clusterID string) (*InventorySyncRequest, error)
	UpdateStatus(ctx context.Context, id string, status InventorySyncRequestStatus, lastError string) error
}

// InventoryQuery describes a stable page over one customer and cluster scope.
type InventoryQuery struct {
	CustomerID string
	ClusterID  string
	Status     InventoryStatus
	NameSearch string
	PageSize   int
	Cursor     string
}

// InventoryPage is one stable page of release inventory results.
type InventoryPage struct {
	Items      []*ReleaseInventory
	NextCursor string
	TotalCount int
	LastSyncAt time.Time
}

// OperationStore defines the persistence contract for operations.
type OperationStore interface {
	Create(ctx context.Context, op *Operation) error
	CreateIfAvailable(ctx context.Context, op *Operation) error
	Get(ctx context.Context, id string) (*Operation, error)
	GetByIdempotencyKey(ctx context.Context, key string) (*Operation, error)
	UpdateStatus(ctx context.Context, id string, status OperationStatus, stateVersion int, lastError string) (*Operation, error)
	Transition(ctx context.Context, id string, status OperationStatus, stateVersion int, lastError string) (*Operation, error)
	GetCancelReplay(ctx context.Context, query OperationCancelReplayQuery) (*OperationCancelResult, error)
	Cancel(ctx context.Context, command OperationCancelCommand) (*OperationCancelResult, error)
	HasActiveForDefinition(ctx context.Context, definitionID string) (bool, error)
	HasActiveEmergencyForDefinition(ctx context.Context, definitionID string) (bool, error)
	List(ctx context.Context, definitionID string) ([]*Operation, error)
	ListNonTerminal(ctx context.Context) ([]*Operation, error)
}

// OperationStateChangedEvent is emitted when an operation's status changes (REQ-023).
// It intentionally excludes values and Secret material.
type OperationStateChangedEvent struct {
	ID            string          `json:"id"`
	OperationID   string          `json:"operation_id"`
	OperationType OperationType   `json:"operation_type"`
	DefinitionID  string          `json:"release_definition_id"`
	OldStatus     OperationStatus `json:"old_status"`
	NewStatus     OperationStatus `json:"new_status"`
	StateVersion  int             `json:"state_version"`
	CreatedAt     time.Time       `json:"created_at"`
}

// OperationEventStore persists operation state change events.
type OperationEventStore interface {
	Create(ctx context.Context, ev *OperationStateChangedEvent) error
}

// DefinitionStore defines the persistence contract for release definitions.
type DefinitionStore interface {
	Create(ctx context.Context, def *ReleaseDefinition, event *ReleaseDefinitionEvent) error
	Get(ctx context.Context, id string) (*ReleaseDefinition, error)
	Update(ctx context.Context, def *ReleaseDefinition, event *ReleaseDefinitionEvent) (*ReleaseDefinition, error)
	List(ctx context.Context, customerID, clusterID string, includeDisabled bool) ([]*ReleaseDefinition, error)
	SetCurrentBundle(ctx context.Context, defID string, bundleID string) (bool, error)
}

// ReleaseDefinitionEvent is emitted for release definition lifecycle changes.
type ReleaseDefinitionEvent struct {
	ID           string    `json:"id"`
	DefinitionID string    `json:"definition_id"`
	EventType    string    `json:"event_type"`
	CreatedAt    time.Time `json:"created_at"`
}

// DefinitionEventStore provides read access to persisted definition events.
type DefinitionEventStore interface {
	List(ctx context.Context, definitionID string) ([]*ReleaseDefinitionEvent, error)
}

// ValuesStore defines the persistence contract for values revisions.
// For Create, the caller MUST populate Revision via GetNextRevisionNumber
// and Digest via the values package before calling.
type ValuesStore interface {
	Create(ctx context.Context, vr *ValuesRevision) error
	// CreateIfParentVersion atomically validates the parent version, allocates
	// the next revision number, and persists the draft.
	CreateIfParentVersion(ctx context.Context, vr *ValuesRevision, expectedParentVersion int) error
	Get(ctx context.Context, id string) (*ValuesRevision, error)
	GetByDigest(ctx context.Context, definitionID, digest string) (*ValuesRevision, error)
	GetLatestApproved(ctx context.Context, definitionID string) (*ValuesRevision, error)
	GetNextRevisionNumber(ctx context.Context, definitionID string) (int, error)
	List(ctx context.Context, definitionID string) ([]*ValuesRevision, error)
	Approve(ctx context.Context, id string, expectedVersion int, approvedBy string) (*ValuesRevision, *ValuesRevision, error)
	Reject(ctx context.Context, id string, expectedVersion int, rejectedBy, reason string) (*ValuesRevision, error)
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
	Update(ctx context.Context, c *Cluster, expectedVersion int64) error
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
	UpdateSequence(ctx context.Context, id string, sequence int64) error
	UpdateStatus(ctx context.Context, id string, status CommandStatus, resultJSON string) error
	GetNextPending(ctx context.Context, operatorID string) (*OutboxEntry, error)
}

// UserStore defines the persistence contract for local and external user accounts (REQ-025, REQ-028).
type UserStore interface {
	Create(ctx context.Context, u *User) error
	Get(ctx context.Context, id string) (*User, error)
	GetByUsername(ctx context.Context, username string) (*User, error)
	GetByProviderSubject(ctx context.Context, provider, subject string) (*User, error)
	Update(ctx context.Context, u *User) error
	Count(ctx context.Context, orgID string) (int64, error)
}

// AuthSessionStore defines the persistence contract for auth sessions (REQ-025).
type AuthSessionStore interface {
	Create(ctx context.Context, s *AuthSession) error
	Get(ctx context.Context, id string) (*AuthSession, error)
	GetByRefreshHash(ctx context.Context, hash string) (*AuthSession, error)
	GetByTokenFamily(ctx context.Context, family string) ([]*AuthSession, error)
	RevokeFamily(ctx context.Context, family string) error
	HasActiveByUserID(ctx context.Context, userID string) (bool, error)
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
	ListByCustomer(ctx context.Context, customerID string) ([]*OrgCustomerBinding, error)
	Update(ctx context.Context, b *OrgCustomerBinding) error
	SetStatus(ctx context.Context, id string, s BindingStatus) error
	RequireActive(ctx context.Context, orgID, customerID string) error
}

// AuditEventStore defines the persistence contract for audit events (REQ-050).
type AuditEventStore interface {
	Create(ctx context.Context, e *AuditEvent) error
	CreateBatch(ctx context.Context, events []*AuditEvent) error
	Query(ctx context.Context, filter AuditEventFilter, cursor string, limit int) (*AuditEventPage, error)
	GetByID(ctx context.Context, id string) (*AuditEvent, error)
	Count(ctx context.Context, filter AuditEventFilter) (int64, error)
	ListByResource(ctx context.Context, resourceType, resourceID string) ([]*AuditEvent, error)
	ListOlderThan(ctx context.Context, cutoff time.Time, batchSize int) ([]*AuditEvent, error)
	DeleteByIDs(ctx context.Context, ids []string) (int64, error)
}

type AuditExportStore interface {
	CreateWithEvent(ctx context.Context, exportRecord *AuditExport, event *AuditEvent) error
}

// NotificationStore defines the persistence contract for notification jobs (REQ-031).
type NotificationStore interface {
	Create(ctx context.Context, j *NotificationJob) error
	Get(ctx context.Context, id string) (*NotificationJob, error)
	GetPending(ctx context.Context, now time.Time, limit int) ([]*NotificationJob, error)
	UpdateStatus(ctx context.Context, id string, status NotificationStatus, attempts int, retryCount int, errorCode string, nextRetryAt *time.Time, lastError string, sentAt *time.Time) error
	MarkDeadLetter(ctx context.Context, id string, errorCode, lastError string) error
	ClaimNext(ctx context.Context, now time.Time) (*NotificationJob, error)
	DeleteDeadLetterBefore(ctx context.Context, before time.Time) (int64, error)
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

// ScanResultStore defines the persistence contract for vulnerability scan results.
type ScanResultStore interface {
	Create(ctx context.Context, rec *ScanResultRecord) error
	GetLatest(ctx context.Context, artifactDigest, scanner string) (*ScanResultRecord, error)
}

// VulnerabilityExceptionStore defines the persistence contract for vulnerability exceptions.
type VulnerabilityExceptionStore interface {
	Create(ctx context.Context, exc *VulnerabilityExceptionRecord) error
	ListByArtifact(ctx context.Context, artifactDigest string) ([]*VulnerabilityExceptionRecord, error)
	Get(ctx context.Context, id string) (*VulnerabilityExceptionRecord, error)
}

// --- Cluster artifact routing domain types (REQ-014) ---

// ArtifactType classifies the kind of artifact routed or indexed by the service.
type ArtifactType string

const (
	ArtifactImage      ArtifactType = "image"
	ArtifactChart      ArtifactType = "chart"
	ArtifactSBOM       ArtifactType = "sbom"
	ArtifactProvenance ArtifactType = "provenance"
	ArtifactSignature  ArtifactType = "signature"
)

// Valid returns true if the artifact type is a recognized value.
func (t ArtifactType) Valid() bool {
	switch t {
	case ArtifactImage, ArtifactChart, ArtifactSBOM, ArtifactProvenance, ArtifactSignature:
		return true
	default:
		return false
	}
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

	// Query returns one filtered page and validates that an opaque cursor still
	// belongs to the same scope, filters, and inventory snapshot.
	Query(ctx context.Context, query InventoryQuery) (*InventoryPage, error)

	// MarkMissing sets InventoryMissing for all rows in a cluster not present in the given set.
	// Returns the count of rows marked missing.
	MarkMissing(ctx context.Context, customerID, clusterID string, presentKeys []string) (int, error)

	// CreateSyncLog records a sync attempt for idempotency.
	// Returns true if inserted (first time), false if already exists.
	CreateSyncLog(ctx context.Context, log *InventorySyncLog) (bool, error)

	// GetBySyncID checks whether a sync_id has already been applied.
	GetBySyncID(ctx context.Context, syncID string) (*InventorySyncLog, error)
	GetByDefinition(ctx context.Context, definitionID string) (*ReleaseInventory, error)
}

// OperationExecutionResultStore provides typed result lookup.
type OperationExecutionResultStore interface {
	Get(ctx context.Context, operationID string) (*OperationExecutionResult, error)
}

// RolloutTrackingStore provides rollout tracking lookup.
type RolloutTrackingStore interface {
	Get(ctx context.Context, operationID string) (*RolloutTracking, error)
}

// UpgradeResultStore atomically persists typed upgrade terminal projections.
type UpgradeResultStore interface {
	FinalizeUpgrade(ctx context.Context, input *UpgradeTerminalInput) error
}

// ── Candidate artifact and ingestion domain types (REQ-011) ───────

// CandidateArtifact represents a globally unique immutable artifact identity.
type CandidateArtifact struct {
	ID           string
	ArtifactType ArtifactType
	Ref          string
	Digest       string
	CreatedAt    time.Time
	LastSeenAt   time.Time
	OrphanedAt   *time.Time
	BundleID     *string // legacy SQLite compatibility; PostgreSQL uses bundle_candidate_artifacts
	ValidatedAt  *time.Time
	SourceID     string
}

// ArtifactDigest identifies one globally unique candidate artifact.
type ArtifactDigest struct {
	Digest       string
	ArtifactType ArtifactType
}

// IdempotencyRecord stores a replayable response for a scoped request key.
type IdempotencyRecord struct {
	Scope       string
	Key         string
	RequestHash string
	ResponseRef json.RawMessage
	ExpiresAt   time.Time
}

// BundleSubmission is the atomic input for a new bundle and its derived records.
type BundleSubmission struct {
	Bundle          *ReleaseBundle
	Candidates      []*CandidateArtifact
	Idempotency     *IdempotencyRecord
	Audit           *ApprovalOutboxEntry
	ValidationEntry *ValidationOutboxEntry
}

// ArtifactEvent is the immutable record of an external artifact observation.
type ArtifactEvent struct {
	ID            string
	SourceID      string
	EventID       string
	EventType     string
	OccurredAt    time.Time
	ReceivedAt    time.Time
	RawPayload    string
	PayloadSHA256 string
	ArtifactType  ArtifactType
	Repository    string
}

// ValidationOutboxStatus is the durable validation worker state.
type ValidationOutboxStatus string

const (
	ValidationPending   ValidationOutboxStatus = "pending"
	ValidationRunning   ValidationOutboxStatus = "running"
	ValidationFailed    ValidationOutboxStatus = "failed"
	ValidationCompleted ValidationOutboxStatus = "completed"
)

// ValidationOutboxEntry schedules source registry validation for a bundle.
type ValidationOutboxEntry struct {
	ID            string
	BundleID      string
	Status        ValidationOutboxStatus
	Attempts      int
	LastErrorCode string
	NextAttemptAt time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// BundleSubmissionStore executes one complete SubmitBundle transaction.
type BundleSubmissionStore interface {
	Submit(ctx context.Context, submission BundleSubmission) (bundle *ReleaseBundle, created bool, err error)
}

// ArtifactEventSubmission atomically records one event and its candidate locations.
type ArtifactEventSubmission struct {
	Event      *ArtifactEvent
	Candidates []*CandidateArtifact
	Audit      *ApprovalOutboxEntry
}

// ArtifactEventSubmissionResult reports event and candidate idempotency effects.
type ArtifactEventSubmissionResult struct {
	Event            *ArtifactEvent
	Created          bool
	NewCandidates    int32
	UpdatedLocations int32
}

// ArtifactEventSubmissionStore executes one complete RecordArtifactEvent transaction.
type ArtifactEventSubmissionStore interface {
	Record(ctx context.Context, submission ArtifactEventSubmission) (*ArtifactEventSubmissionResult, error)
}

// CandidateArtifactStore defines the persistence contract for candidate artifacts.
type CandidateArtifactStore interface {
	Create(ctx context.Context, ca *CandidateArtifact) error
	Get(ctx context.Context, id string) (*CandidateArtifact, error)
	ListValidated(ctx context.Context) ([]*CandidateArtifact, error)
	LinkToBundle(ctx context.Context, artifactID, bundleID string) error
	UpsertTx(tx *gorm.DB, ca *CandidateArtifact) error
	UpsertLocationTx(tx *gorm.DB, artifactID, ref, sourceID string, now time.Time) error
	LinkToBundleTx(tx *gorm.DB, bundleID string, digests []ArtifactDigest) error
	DeleteOrphanBefore(ctx context.Context, cutoff time.Time, limit ...int) (int64, error)
}

// ArtifactEventStore persists externally observed artifact events.
type ArtifactEventStore interface {
	CreateTx(tx *gorm.DB, event *ArtifactEvent) error
	GetBySourceAndEvent(ctx context.Context, sourceID, eventID string) (*ArtifactEvent, error)
}

// ValidationOutboxStore persists and claims bundle validation work.
type ValidationOutboxStore interface {
	CreateTx(tx *gorm.DB, entry *ValidationOutboxEntry) error
	ClaimPending(ctx context.Context, now time.Time, limit int) ([]ValidationOutboxEntry, error)
	UpdateTx(tx *gorm.DB, entry *ValidationOutboxEntry) error
}

// ── Preflight lifecycle domain types (REQ-069) ─────────────────────

// PreflightLifecycle records the lifecycle of a preflight check for GC.
type PreflightLifecycle struct {
	ID                  string
	OperationID         *string
	OperationTerminalAt *time.Time
	Stages              json.RawMessage
	Overall             string
	ErrorCode           string
	CreatedAt           time.Time
}

// PreflightLifecycleStore defines the persistence contract for preflight lifecycles.
type PreflightLifecycleStore interface {
	Create(ctx context.Context, pl *PreflightLifecycle) error
	SetOperationTerminal(ctx context.Context, operationID string, terminalAt time.Time) error
	DeleteExpired(ctx context.Context, ttl time.Duration) (int64, error)
}

// Store is the top-level persistence abstraction.
type Store interface {
	Operations() OperationStore
	OperationEvents() OperationEventStore
	Timeline() TimelineStore
	Definitions() DefinitionStore
	DefinitionEvents() DefinitionEventStore
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
	AuditExports() AuditExportStore
	TrustRoots() TrustRootStore
	Notifications() NotificationStore
	Bundles() BundleStore
	ScanResults() ScanResultStore
	VulnerabilityExceptions() VulnerabilityExceptionStore
	Verifications() VerificationStore
	PreflightResults() PreflightStore
	CustomerEvents() CustomerEventStore
	ClusterRoutes() ClusterRouteStore
	Inventories() InventoryStore
	InventorySyncRequests() InventorySyncRequestStore
	ExecutionResults() OperationExecutionResultStore
	RolloutTrackings() RolloutTrackingStore
	UpgradeResults() UpgradeResultStore
	CandidateArtifacts() CandidateArtifactStore
	ArtifactEvents() ArtifactEventStore
	ValidationOutbox() ValidationOutboxStore
	BundleSubmissions() BundleSubmissionStore
	ArtifactEventSubmissions() ArtifactEventSubmissionStore
	PreflightLifecycles() PreflightLifecycleStore
	EmergencyIntents() EmergencyIntentStore
	ConvergenceTasks() ConvergenceTaskStore
	Authorization() AuthorizationStore
	Close() error
}
