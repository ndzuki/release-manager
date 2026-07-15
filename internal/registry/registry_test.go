package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServicesCount(t *testing.T) {
	assert.Len(t, Services(), 7, "should have 7 registered services per REQ-002")
}

func TestServiceNamesUnique(t *testing.T) {
	seen := make(map[string]bool)
	for _, s := range Services() {
		assert.False(t, seen[s.Name], "duplicate service name: %s", s.Name)
		seen[s.Name] = true
	}
}

func TestRequirementsFor(t *testing.T) {
	tests := []struct {
		service string
		wantLen int
	}{
		{"release-webhook", 3},
		{"release-orchestrator", 28},
		{"release-operator", 19},
		{"release-auth", 7},
		{"release-api", 1},
		{"release-notifier", 1},
		{"web", 11},
		{"unknown", 0},
	}
	for _, tt := range tests {
		t.Run(tt.service, func(t *testing.T) {
			got := RequirementsFor(tt.service)
			assert.Len(t, got, tt.wantLen)
		})
	}
}

func TestServiceFor(t *testing.T) {
	// Spot-check a few requirement-to-service mappings from REQ-002.
	// When requirements overlap across services, ServiceFor returns
	// the first match in Services() order.
	tests := []struct {
		reqID   string
		service string
	}{
		{"010", "release-webhook"},
		{"014", "release-orchestrator"},
		{"041", "release-orchestrator"},
		{"048", "release-orchestrator"}, // shared with operator
		{"025", "release-auth"},
		{"031", "release-orchestrator"}, // shared with notifier
		{"033", "web"},
		{"060", "web"},
		{"999", ""},
	}
	for _, tt := range tests {
		t.Run(tt.reqID, func(t *testing.T) {
			s := ServiceFor(tt.reqID)
			if tt.service == "" {
				assert.Nil(t, s)
			} else {
				assert.NotNil(t, s)
				assert.Equal(t, tt.service, s.Name)
			}
		})
	}
}
