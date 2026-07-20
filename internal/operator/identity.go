package operator

import (
	"context"
)

type certificateIdentity struct {
	Serial string
}

type identityContextKey struct{}

func WithCertificateIdentity(ctx context.Context, serial string) context.Context {
	return context.WithValue(ctx, identityContextKey{}, certificateIdentity{Serial: serial})
}
