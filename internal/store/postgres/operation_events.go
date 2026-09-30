package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

// insertOperationEvent is the only writer of operation events: the operation state
// machine calls it inside its transitions (see operations.go and emergency_converge.go).
func insertOperationEvent(ctx context.Context, execer operationExecer, ev *store.OperationStateChangedEvent) error {
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now().UTC()
	}

	_, err := execer.ExecContext(ctx, `
INSERT INTO operation_events (id, operation_id, operation_type, release_definition_id, old_status, new_status, state_version, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`,
		ev.ID, ev.OperationID, string(ev.OperationType), ev.DefinitionID,
		string(ev.OldStatus), string(ev.NewStatus), ev.StateVersion,
		ev.CreatedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("insert operation event: %w", err)
	}
	return nil
}
