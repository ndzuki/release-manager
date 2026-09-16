package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/config"
)

// TestNotificationSinkReadinessChecksConfig pins TASK-099 AC3 for the sink:
// the dev-only test double has no database or upstream, so its one real
// precondition is a decoded servable config — not the old noop.
func TestNotificationSinkReadinessChecksConfig(t *testing.T) {
	t.Parallel()

	check := (&notificationSink{capacity: 100, cfg: config.ServiceConfig{HTTPPort: 8084}}).
		ReadinessChecks()["config"]
	require.NoError(t, check())

	broken := (&notificationSink{capacity: 100}).ReadinessChecks()["config"]
	require.Error(t, broken(), "http_port must fail closed when missing")
}
