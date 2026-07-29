// Package registry provides domain navigation — service descriptors and
// atomic-requirement-to-service mappings per REQ-002.
package registry

// ServiceDescriptor describes a microservice in the release-manager system.
type ServiceDescriptor struct {
	// Name is the service identifier (e.g. "release-webhook").
	Name string
	// Description is a human-readable summary.
	Description string
	// ConnectService is the fully-qualified Connect service name (optional).
	ConnectService string
	// Requirements lists the atomic requirement IDs owned by this service.
	Requirements []string
}

// Services returns the canonical list of microservice descriptors.
func Services() []ServiceDescriptor {
	return []ServiceDescriptor{
		{
			Name:           "release-webhook",
			Description:    "Handles artifact ingestion from external sources (e.g. Harbor).",
			ConnectService: "webhook.v1.WebhookService",
			Requirements:   []string{"010", "011", "012"},
		},
		{
			Name:           "release-orchestrator",
			Description:    "Coordinates release publish workflows across services.",
			ConnectService: "orchestrator.v1.OrchestratorService",
			Requirements: []string{
				"013", "014", "015", "016", "017", "018", "019",
				"020", "021", "022", "023", "024", "027", "029",
				"030", "031", "032", "040", "041", "042", "043",
				"044", "045", "046", "047", "048", "049", "050",
			},
		},
		{
			Name:           "release-operator",
			Description:    "Manages bidirectional gRPC streams with operator agents.",
			ConnectService: "operator.v1.OperatorService",
			Requirements: []string{
				"015", "016", "017", "018", "019", "020", "021",
				"022", "023", "024", "032", "041", "042", "043",
				"044", "045", "046", "047", "048",
			},
		},
		{
			Name:           "release-auth",
			Description:    "Handles authentication, authorization, and token management.",
			ConnectService: "auth.v1.AuthService",
			Requirements: []string{
				"025", "026", "027", "028", "029", "049", "050",
			},
		},
		{
			Name:           "release-notifier",
			Description:    "Dispatches notifications about release events.",
			ConnectService: "notifier.v1.NotifierService",
			Requirements:   []string{"031"},
		},
		{
			Name:        "web",
			Description: "Frontend UI for the release manager.",
			Requirements: []string{
				"033", "051", "052", "053", "054", "055", "056",
				"057", "058", "059", "060",
			},
		},
	}
}

// RequirementsFor returns the atomic requirement IDs for a service by name.
// Returns nil if the service is unknown.
func RequirementsFor(name string) []string {
	for _, s := range Services() {
		if s.Name == name {
			return s.Requirements
		}
	}
	return nil
}

// ServiceFor returns the service descriptor that owns the given requirement.
// Returns nil if no service owns the requirement.
func ServiceFor(reqID string) *ServiceDescriptor {
	for i := range Services() {
		s := &Services()[i]
		for _, r := range s.Requirements {
			if r == reqID {
				return s
			}
		}
	}
	return nil
}
