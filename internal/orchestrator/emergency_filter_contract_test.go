package orchestrator

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// emergencyStatusFilters are the values ListConvergenceTasks accepts. The server
// rejects anything else with invalid_status_filter, so a client that sends a
// different spelling gets an empty, always-failing page.
var emergencyStatusFilters = []string{"pending_promotion", "converged"}

// The web client and the server are different languages, so nothing else pins
// this contract: the client sent 'PENDING_PROMOTION' while the server accepted
// only the snake_case values, and ConvergenceTasksPage failed to load on every
// request. This test fails if the two spellings drift apart again.
func TestWebConvergenceStatusFilterMatchesTheServerContract(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../web/src/connect/emergency-api.ts")
	require.NoError(t, err, "the web client must stay readable from this test")

	matches := regexp.MustCompile(`statusFilter:\s*'([^']+)'`).FindAllStringSubmatch(string(raw), -1)
	require.NotEmpty(t, matches, "expected a statusFilter literal in emergency-api.ts")

	for _, match := range matches {
		value := match[1]
		if value == "" {
			continue // an empty filter means "no filter", which the server allows
		}
		assert.Contains(t, emergencyStatusFilters, value,
			"the web client sends statusFilter=%q, which the server rejects with invalid_status_filter", value)
		assert.Equal(t, strings.ToLower(value), value,
			"the server compares the filter verbatim; %q must be snake_case", value)
	}
}
