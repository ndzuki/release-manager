// Package operation implements the core Operation state machine (REQ-023).
package operation

import (
	"errors"
	"fmt"

	"github.com/ndzuki/release-manager/internal/store"
)

// Event represents a stimulus that triggers a state transition.
type Event string

const (
	EventStartPreflight    Event = "start_preflight"
	EventPreflightPassed   Event = "preflight_passed"
	EventEnqueue           Event = "enqueue"
	EventBegin             Event = "begin"
	EventComplete          Event = "complete"
	EventError             Event = "error"
	EventCancel            Event = "cancel"
	EventAcknowledgeCancel Event = "acknowledge_cancel"
	EventTimeout           Event = "timeout"
)

// Sentinel errors returned by the state machine.
var (
	ErrInvalidTransition = errors.New("invalid state transition")
	ErrCancelNotAllowed  = errors.New("cancel not allowed")
)

// validTransitions maps current status + event to the next status.
var validTransitions = map[store.OperationStatus]map[Event]store.OperationStatus{
	store.StatusPending: {
		EventStartPreflight: store.StatusPreflight,
		EventEnqueue:        store.StatusQueued, // EMERGENCY path
		EventCancel:         store.StatusCancelled,
		EventTimeout:        store.StatusTimeout,
	},
	store.StatusPreflight: {
		EventPreflightPassed: store.StatusQueued,
		EventError:           store.StatusFailed,
		EventCancel:          store.StatusCancelled,
		EventTimeout:         store.StatusTimeout,
	},
	store.StatusQueued: {
		EventBegin:   store.StatusRunning,
		EventCancel:  store.StatusCancelled,
		EventTimeout: store.StatusTimeout,
	},
	store.StatusRunning: {
		EventComplete: store.StatusSucceeded,
		EventError:    store.StatusFailed,
		EventCancel:   store.StatusCancelling,
		EventTimeout:  store.StatusTimeout,
	},
	store.StatusCancelling: {
		EventAcknowledgeCancel: store.StatusCancelled,
		EventError:             store.StatusFailed,
		EventTimeout:           store.StatusTimeout,
	},
}

// Transition validates and returns the next status given the current status and event.
// Returns an error if the transition is not allowed.
func Transition(current store.OperationStatus, event Event) (store.OperationStatus, error) {
	if current.IsTerminal() {
		return current, fmt.Errorf("%w: %s is terminal", ErrInvalidTransition, current)
	}

	transitions, ok := validTransitions[current]
	if !ok {
		return current, fmt.Errorf("%w: unknown status %s", ErrInvalidTransition, current)
	}

	next, ok := transitions[event]
	if !ok {
		return current, fmt.Errorf("%w: %s from %s", ErrInvalidTransition, event, current)
	}

	return next, nil
}

// ValidPathForType returns the valid state path events for a given operation type.
// Standard operations (INSTALL/UPGRADE/ROLLBACK) go through preflight.
// EMERGENCY operations skip preflight and go directly from pending to queued.
func ValidPathForType(opType store.OperationType) []Event {
	if opType.IsStandard() {
		return []Event{
			EventStartPreflight, EventPreflightPassed, EventEnqueue,
			EventBegin, EventComplete,
		}
	}
	// EMERGENCY
	return []Event{
		EventEnqueue, EventBegin, EventComplete,
	}
}

// InitialStatus returns the initial status for an operation.
func InitialStatus() store.OperationStatus {
	return store.StatusPending
}

// CanCancel returns true if the operation can be cancelled from its current status.
func CanCancel(current store.OperationStatus) bool {
	transitions, ok := validTransitions[current]
	if !ok {
		return false
	}
	_, hasCancel := transitions[EventCancel]
	return hasCancel
}
