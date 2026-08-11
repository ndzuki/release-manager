// Package localstore provides a BoltDB-backed persistent command store
// for the operator agent. It stores received commands so that the operator
// can replay uncompleted commands after a restart, and supports fsync-based
// ACK_PERSISTED acknowledgments.
package localstore

import "context"

// CommandEntry is a locally persisted command record.
type CommandEntry struct {
	CommandID     string `json:"command_id"`
	OutboxID      string `json:"outbox_id"`
	OperationID   string `json:"operation_id"`
	OperationType string `json:"operation_type"`
	Sequence      int64  `json:"sequence"`
	Payload       []byte `json:"payload"`
	Status        string `json:"status"` // pending, running, succeeded, failed
	ResultJSON    string `json:"result_json,omitempty"`
}

// Status values for locally stored commands.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// IsTerminal returns true if the status is a final state.
func IsTerminal(status string) bool {
	return status == StatusSucceeded || status == StatusFailed
}

// Store provides persistent storage for operator commands.
// It uses BoltDB for ACID guarantees and supports fsync for
// ACK_PERSISTED semantics.
type Store interface {
	// Save persists a command entry. It must fsync before returning.
	Save(ctx context.Context, e *CommandEntry) error

	// Get retrieves a command by its command_id.
	Get(ctx context.Context, commandID string) (*CommandEntry, error)

	// GetByOutboxID retrieves a command by outbox_id.
	GetByOutboxID(ctx context.Context, outboxID string) (*CommandEntry, error)

	// UpdateStatus updates the status and optional result of a command.
	UpdateStatus(ctx context.Context, commandID string, status string, resultJSON string) error

	// ListActive returns all commands that are NOT in a terminal state.
	// Used on restart to replay uncompleted work.
	ListActive(ctx context.Context) ([]*CommandEntry, error)

	// LastSequence returns the highest sequence number stored.
	// Returns 0 if no commands are stored.
	LastSequence(ctx context.Context) (int64, error)

	// Close releases the database resources.
	Close() error
}

// Identity is the persisted operator bootstrap identity (TASK-075). It is
// stored in the same BoltDB file as commands but in a dedicated bucket, and
// must never be logged or transmitted (REQ-015 AC-015-05: the private key
// stays inside the customer cluster).
type Identity struct {
	OperatorID     string `json:"operator_id"`
	OperatorName   string `json:"operator_name"`
	CustomerID     string `json:"customer_id"`
	ClusterID      string `json:"cluster_id"`
	SessionID      string `json:"session_id"`
	PrivateKeyPEM  string `json:"private_key_pem"`
	CertificatePEM string `json:"certificate_pem"`
}

// IdentityStore persists the operator identity across restarts so the agent
// reconnects with the enrolled certificate instead of re-enrolling
// (REQ-015/REQ-044 contract, AC-075-02).
type IdentityStore interface {
	// SaveIdentity durably persists the identity (fsync before returning).
	SaveIdentity(ctx context.Context, identity *Identity) error

	// LoadIdentity returns the persisted identity, or ErrNotFound when the
	// agent has never bootstrapped.
	LoadIdentity(ctx context.Context) (*Identity, error)
}
