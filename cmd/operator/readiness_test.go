package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatoragent "github.com/ndzuki/release-manager/internal/operator/agent"
)

// TestOperatorReadinessGatesOnGatewaySession pins TASK-099 AC3 for the
// operator agent: agent mode exposes a gateway_session check that fails while
// no live session exists (the connected-flag lifecycle itself is proven in
// the agent package, TestRunTracksConnectedWhileSessionLive). Gateway mode
// has no outbound session and keeps the noop baseline.
func TestOperatorReadinessGatesOnGatewaySession(t *testing.T) {
	t.Parallel()

	gateway := &operatorSvc{mode: "gateway"}
	assert.Nil(t, gateway.ReadinessChecks(), "gateway mode must not advertise a session check")

	agentMode := &operatorSvc{mode: "agent", agent: &operatoragent.Agent{}}
	check, ok := agentMode.ReadinessChecks()["gateway_session"]
	require.True(t, ok, "agent mode must expose the gateway_session check")
	require.Error(t, check(), "a zero-value agent has no session; /readyz must fail closed")
}
