package sqlite_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

// AC-031-05: a terminal transition queues exactly one OperationTerminal
// notification, in the same transaction; a non-terminal one queues none.
func TestOperationTransition_QueuesTerminalNotification(t *testing.T) {
	st := setupStore(t)
	ctx := context.Background()
	def := createTestDefinition(t, st)
	op := &store.Operation{
		ID:                  "op-terminal-notify",
		OperationType:       store.OperationInstall,
		Status:              store.StatusRunning,
		ReleaseDefinitionID: def.ID,
		IdempotencyKey:      "op-terminal-notify-key",
		RequestHash:         "request-hash",
		StateVersion:        1,
	}
	require.NoError(t, st.Operations().Create(ctx, op))

	count := func() int {
		var n int
		require.NoError(t, st.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM notification_outbox WHERE event_type = 'OperationTerminal'`).Scan(&n))
		return n
	}
	assert.Equal(t, 0, count(), "a running operation queues nothing")

	// running -> succeeded is the path the existing transition test uses.
	_, err := st.Operations().Transition(ctx, op.ID, store.StatusSucceeded, op.StateVersion, "")
	require.NoError(t, err)
	assert.Equal(t, 1, count(), "a terminal transition must queue exactly one notification")

	// The payload must follow REQ-031's OperationTerminal contract: the status is
	// terminal_status, not status.
	var payload string
	require.NoError(t, st.DB().QueryRowContext(ctx,
		`SELECT payload_json FROM notification_outbox WHERE event_type = 'OperationTerminal'`).Scan(&payload))
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &decoded))
	assert.Equal(t, "OperationTerminal", decoded["event_type"])
	assert.Equal(t, "succeeded", decoded["terminal_status"], "REQ-031 names this field terminal_status")
	assert.Equal(t, string(store.OperationInstall), decoded["operation_type"])
	assert.NotContains(t, decoded, "status", "the contract has no bare status field")
}
