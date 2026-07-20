package audit

import "fmt"

// EventError carries a stable code and the rejected event identity.
type EventError struct {
	Code ErrorCode
	ID   string
	Err  error
}

func (e *EventError) Error() string {
	if e.ID == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: event_id=%s", e.Code, e.ID)
}

func (e *EventError) Unwrap() error { return e.Err }
