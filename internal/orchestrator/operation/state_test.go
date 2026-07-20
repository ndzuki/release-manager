package operation

import (
	"testing"

	"github.com/ndzuki/release-manager/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransition_StandardFullPath(t *testing.T) {
	// AC-003-02: pending → preflight → queued → running → succeeded
	path := []struct {
		event Event
		next  store.OperationStatus
	}{
		{EventStartPreflight, store.StatusPreflight},
		{EventPreflightPassed, store.StatusQueued},
		{EventBegin, store.StatusRunning},
		{EventComplete, store.StatusSucceeded},
	}

	current := store.StatusPending
	for _, step := range path {
		next, err := Transition(current, step.event)
		require.NoError(t, err, "transition %s from %s", step.event, current)
		assert.Equal(t, step.next, next)
		current = next
	}
	assert.True(t, current.IsTerminal())
}

func TestTransition_EmergencyPath(t *testing.T) {
	// EMERGENCY skips preflight: pending → queued → running → succeeded
	path := []struct {
		event Event
		next  store.OperationStatus
	}{
		{EventEnqueue, store.StatusQueued},
		{EventBegin, store.StatusRunning},
		{EventComplete, store.StatusSucceeded},
	}

	current := store.StatusPending
	for _, step := range path {
		next, err := Transition(current, step.event)
		require.NoError(t, err, "transition %s from %s", step.event, current)
		assert.Equal(t, step.next, next)
		current = next
	}
}

func TestTransition_FailAndTimeout(t *testing.T) {
	// running → failed
	next, err := Transition(store.StatusRunning, EventError)
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, next)

	// running → timeout
	next, err = Transition(store.StatusRunning, EventTimeout)
	require.NoError(t, err)
	assert.Equal(t, store.StatusTimeout, next)

	// preflight → failed (AC-019-01)
	next, err = Transition(store.StatusPreflight, EventError)
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, next)

	// preflight → timeout
	next, err = Transition(store.StatusPreflight, EventTimeout)
	require.NoError(t, err)
	assert.Equal(t, store.StatusTimeout, next)
}

func TestTransition_Cancel(t *testing.T) {
	// AC-023-04: running cancel → cancelling (then ACK → cancelled)
	next, err := Transition(store.StatusRunning, EventCancel)
	require.NoError(t, err)
	assert.Equal(t, store.StatusCancelling, next)

	// cancelling → cancelled
	next, err = Transition(store.StatusCancelling, EventAcknowledgeCancel)
	require.NoError(t, err)
	assert.Equal(t, store.StatusCancelled, next)

	// cancelling error
	next, err = Transition(store.StatusCancelling, EventError)
	require.NoError(t, err)
	assert.Equal(t, store.StatusFailed, next)

	// pending direct cancel
	next, err = Transition(store.StatusPending, EventCancel)
	require.NoError(t, err)
	assert.Equal(t, store.StatusCancelled, next)

	// preflight cancel
	next, err = Transition(store.StatusPreflight, EventCancel)
	require.NoError(t, err)
	assert.Equal(t, store.StatusCancelled, next)

	// queued cancel
	next, err = Transition(store.StatusQueued, EventCancel)
	require.NoError(t, err)
	assert.Equal(t, store.StatusCancelled, next)
}

func TestTransition_InvalidMoves(t *testing.T) {
	// AC-023-01: illegal backward transitions
	invalidCases := []struct {
		current store.OperationStatus
		event   Event
	}{
		{store.StatusSucceeded, EventComplete},
		{store.StatusFailed, EventError},
		{store.StatusCancelled, EventCancel},
		{store.StatusTimeout, EventTimeout},
		// Can't go backward
		{store.StatusRunning, EventEnqueue},
		{store.StatusPreflight, EventStartPreflight},
		{store.StatusQueued, EventStartPreflight},
		// Standard path pending → queued is not allowed (only EMERGENCY)
		{store.StatusPending, EventBegin},
	}

	for _, tc := range invalidCases {
		t.Run(string(tc.current)+"_"+string(tc.event), func(t *testing.T) {
			_, err := Transition(tc.current, tc.event)
			assert.ErrorIs(t, err, ErrInvalidTransition)
		})
	}
}

func TestTransition_TerminalNoOp(t *testing.T) {
	for _, status := range []store.OperationStatus{
		store.StatusSucceeded,
		store.StatusFailed,
		store.StatusCancelled,
		store.StatusTimeout,
	} {
		_, err := Transition(status, EventComplete)
		assert.ErrorIs(t, err, ErrInvalidTransition, "terminal %s should not transition", status)
	}
}

func TestValidPathForType(t *testing.T) {
	// Standard path includes preflight
	std := ValidPathForType(store.OperationInstall)
	assert.Contains(t, std, EventStartPreflight)
	assert.Contains(t, std, EventPreflightPassed)

	// EMERGENCY skips preflight
	em := ValidPathForType(store.OperationEmergency)
	assert.NotContains(t, em, EventStartPreflight)
	assert.NotContains(t, em, EventPreflightPassed)
	assert.Contains(t, em, EventEnqueue)
}

func TestCanCancel(t *testing.T) {
	cancelable := []store.OperationStatus{
		store.StatusPending,
		store.StatusPreflight,
		store.StatusQueued,
		store.StatusRunning,
	}
	for _, s := range cancelable {
		assert.True(t, CanCancel(s), "%s should be cancelable", s)
	}

	notCancelable := []store.OperationStatus{
		store.StatusCancelling,
		store.StatusSucceeded,
		store.StatusFailed,
		store.StatusCancelled,
		store.StatusTimeout,
	}
	for _, s := range notCancelable {
		assert.False(t, CanCancel(s), "%s should NOT be cancelable", s)
	}
}
