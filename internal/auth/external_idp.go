package auth

import "context"

// ExternalIdP defines the interface for external identity providers (REQ-028).
// Concrete implementations (OIDC, LDAP, DingTalk) are deferred to P1.
type ExternalIdP interface {
	// Provider returns the IdP type (oidc, ldap, dingtalk).
	Provider() string

	// Authenticate validates credentials with the external provider and returns
	// a normalized identity (subject + attributes).
	Authenticate(ctx context.Context, credential any) (*ExternalIdentity, error)

	// Validate checks that the IdP configuration is usable.
	Validate(ctx context.Context) error
}

// ExternalIdentity is the normalized result of an external authentication.
type ExternalIdentity struct {
	Provider   string
	Subject    string
	Attributes map[string]string
}

// NoopIdP is a placeholder that rejects all authentication attempts.
// Replace with real implementations when REQ-028 P1 is addressed.
type NoopIdP struct{ provider string }

func NewNoopIdP(provider string) *NoopIdP { return &NoopIdP{provider: provider} }
func (n *NoopIdP) Provider() string       { return n.provider }
func (n *NoopIdP) Authenticate(_ context.Context, _ any) (*ExternalIdentity, error) {
	return nil, &IdPNotImplementedError{Provider: n.provider}
}
func (n *NoopIdP) Validate(_ context.Context) error { return nil }

// IdPNotImplementedError indicates the IdP integration is deferred.
type IdPNotImplementedError struct{ Provider string }

func (e *IdPNotImplementedError) Error() string {
	return "external IdP " + e.Provider + " not yet implemented (P1 deferred)"
}
