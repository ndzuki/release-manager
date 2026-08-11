package orchestrator

import (
	"context"
	"errors"

	"github.com/ndzuki/release-manager/internal/operator"
)

// OperatorStreamRevoker closes active Operator command streams after Store commit.
type OperatorStreamRevoker interface {
	Revoke(context.Context, string, string) error
}

type processStreamRevoker struct {
	registry *operator.StreamRegistry
}

// NewProcessStreamRevoker adapts the shared in-process StreamRegistry into the
// OperatorStreamRevoker seam. The Orchestrator and the mounted OperatorService
// share one registry (REQ-053), so a committed revocation cancels the live
// command stream in the same process without a second HTTP hop (ADR-001).
func NewProcessStreamRevoker(registry *operator.StreamRegistry) OperatorStreamRevoker {
	return &processStreamRevoker{registry: registry}
}

func (r *processStreamRevoker) Revoke(_ context.Context, operatorID, reason string) error {
	if r == nil || r.registry == nil {
		return errors.New("revoke operator stream: registry is required")
	}
	// Stream close is best-effort after the Store commit: an offline Operator
	// has no registered stream and must not turn a successful revoke into an
	// error (AC-053-14).
	r.registry.Revoke(operatorID, reason)
	return nil
}
