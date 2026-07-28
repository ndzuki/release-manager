// Package authctx exposes the authenticated actor carried between interceptors and handlers.
package authctx

import "context"

type actorKey struct{}

// Actor is the verified identity snapshot for one request.
type Actor struct {
	UserID         string
	OrganizationID string
	Roles          []string
}

// WithActor returns a context containing the verified actor snapshot.
func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFromContext returns the verified actor snapshot.
func ActorFromContext(ctx context.Context) (Actor, bool) {
	actor, ok := ctx.Value(actorKey{}).(Actor)
	if !ok || actor.UserID == "" || actor.OrganizationID == "" {
		return Actor{}, false
	}
	return actor, true
}
