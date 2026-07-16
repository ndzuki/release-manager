// Package localstore provides a BoltDB-backed persistent command store
// for the operator agent. It stores received commands so that the operator
// can replay uncompleted commands after a restart, and supports fsync-based
// ACK_PERSISTED acknowledgments.
package localstore

import "context"

// CommandEntry is a locally persisted command record.
type CommandEntry struct {
	CommandID   string `json:"command_id"`
	OutboxID    string `json:"outbox_id"`
	OperationID string `json:"operation_id"`
	Sequence    int64  `json:"sequence"`
	Payload     []byte `json:"payload"`
	Status      string `json:"status"` // pending, running, succeeded, failed
	ResultJSON  string `json:"result_json,omitempty"`
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
