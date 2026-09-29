// Package authctx exposes the authenticated actor carried between interceptors and handlers.
package authctx

import "context"

type (
	actorKey               struct{}
	authorizationHeaderKey struct{}
	verifiedCredentialKey  struct{}
)

// Actor is the verified identity snapshot for one request.
type Actor struct {
	UserID         string
	OrganizationID string
	Roles          []string
	Service        string
}

// WithActor returns a context containing the verified actor snapshot.
func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFromContext returns the verified actor snapshot.
func ActorFromContext(ctx context.Context) (Actor, bool) {
	actor, ok := ctx.Value(actorKey{}).(Actor)
	if !ok {
		return Actor{}, false
	}
	if actor.Service != "" {
		return actor, true
	}
	if actor.UserID == "" || actor.OrganizationID == "" {
		return Actor{}, false
	}
	return actor, true
}

// WithAuthorizationHeader keeps the verified inbound credential available to internal Connect clients.
func WithAuthorizationHeader(ctx context.Context, value string) context.Context {
	return context.WithValue(ctx, authorizationHeaderKey{}, value)
}

// AuthorizationHeaderFromContext returns the inbound Authorization header for internal forwarding.
func AuthorizationHeaderFromContext(ctx context.Context) string {
	value, ok := ctx.Value(authorizationHeaderKey{}).(string)
	if !ok {
		return ""
	}
	return value
}

// WithVerifiedCredential stores the credential the auth interceptor has already
// verified for this request — either the bearer token or the browser cookie
// token. Downstream internal RPCs must forward THIS, not the raw inbound
// Authorization header: a cookie-authenticated console sends no Authorization
// header at all, so forwarding the header left the credential empty and every
// internal call unauthenticated (ADR-028 clause 5 / B4).
func WithVerifiedCredential(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, verifiedCredentialKey{}, token)
}

// VerifiedCredentialFromContext returns the verified credential, or "" when the
// request was not authenticated through this path.
func VerifiedCredentialFromContext(ctx context.Context) string {
	value, ok := ctx.Value(verifiedCredentialKey{}).(string)
	if !ok {
		return ""
	}
	return value
}
