package auth

import "context"

// CustomerResolver verifies that a customer exists. The real implementation
// calls the orchestrator service via Connect. For now, a stub allows auth
// service testing without a running orchestrator (TASK-004 dependency).
type CustomerResolver interface {
	// Exists returns true if the customer exists and is active.
	Exists(ctx context.Context, customerID string) (bool, error)
}

// StubResolver is a no-op resolver that always returns true.
// Replace with a real Connect client after TASK-004 completes.
type StubResolver struct{}

func (StubResolver) Exists(_ context.Context, _ string) (bool, error) { return true, nil }
