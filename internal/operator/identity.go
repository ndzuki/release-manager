package operator

import (
	"context"
	"errors"
)

var errClientCertificateRequired = errors.New("client certificate required")

type certificateIdentity struct {
	Serial string
}

type identityContextKey struct{}

func WithCertificateIdentity(ctx context.Context, serial string) context.Context {
	return context.WithValue(ctx, identityContextKey{}, certificateIdentity{Serial: serial})
}

func certificateIdentityFromContext(ctx context.Context) (certificateIdentity, error) {
	identity, ok := ctx.Value(identityContextKey{}).(certificateIdentity)
	if !ok || identity.Serial == "" {
		return certificateIdentity{}, errClientCertificateRequired
	}
	return identity, nil
}
