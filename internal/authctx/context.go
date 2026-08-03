// Package authctx exposes the authenticated actor carried between interceptors and handlers.
package authctx

import "context"

type (
	actorKey               struct{}
	authorizationHeaderKey struct{}
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
	value, _ := ctx.Value(authorizationHeaderKey{}).(string)
	return value
}
