package auth

import "context"

// ExternalIdP defines the interface implemented by an external identity provider.
type ExternalIdP interface {
	Provider() string
	Authenticate(ctx context.Context, credential any) (*ExternalIdentity, error)
	Validate(ctx context.Context) error
}

// ExternalIdentity is the normalized result of an external authentication.
type ExternalIdentity struct {
	Provider   string
	Subject    string
	Attributes map[string]string
}
