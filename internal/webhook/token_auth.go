package webhook

import (
	"net/http"
	"strings"
)

// TokenVerifier reports whether a presented bearer token is acceptable.
type TokenVerifier func(token string) bool

// RequireToken guards a plain-HTTP ingress with the same credential contract as
// the Connect service-token legs: `Authorization: Bearer <token>` verified in
// constant time. The Harbor ingress is not a Connect procedure, so it cannot use
// auth.ServiceTokenInterceptor; the verifier is injected so this package stays
// free of the auth package (internal/auth tests import this package).
// An empty/rejecting verifier and every rejection answer 401, so an
// unauthenticated caller learns nothing about which credential family the
// endpoint expects.
func RequireToken(verify TokenVerifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" || verify == nil || !verify(token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
