package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ndzuki/release-manager/internal/store"
)

type operationEventStore struct{ db *sql.DB }

func (s *operationEventStore) Create(ctx context.Context, ev *store.OperationStateChangedEvent) error {
	return insertOperationEvent(ctx, s.db, ev)
}

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
