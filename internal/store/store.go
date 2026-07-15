// Package store defines the persistence layer interfaces and domain types
// for the release-manager core pipeline.
package store

import (
	"context"
	"errors"
	"time"
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

// OperationStore defines the persistence contract for operations.
type OperationStore interface {
	Create(ctx context.Context, op *Operation) error
	Get(ctx context.Context, id string) (*Operation, error)
	GetByIdempotencyKey(ctx context.Context, key string) (*Operation, error)
	UpdateStatus(ctx context.Context, id string, status OperationStatus, stateVersion int, lastError string) (*Operation, error)
	HasActiveForDefinition(ctx context.Context, definitionID string) (bool, error)
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

// Store is the top-level persistence abstraction.
type Store interface {
	Operations() OperationStore
	Definitions() DefinitionStore
	Values() ValuesStore
	Close() error
}
