package auth

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestResolveRequestToken pins the credential-carrier precedence ADR-028 relies
// on: the Authorization header wins, the rm_access cookie is the fallback, and
// a request with neither resolves to the empty token.
//
// The streaming interceptor used to read the header only, so a
// cookie-authenticated console could never open WatchOperation (B3); both
// paths now share this resolver.
func TestResolveRequestToken(t *testing.T) {
	tests := []struct {
		name           string
		header         http.Header
		wantToken      string
		wantFromCookie bool
	}{
		{
			name:      "bearer header",
			header:    http.Header{"Authorization": []string{"Bearer header-token"}},
			wantToken: "header-token",
		},
		{
			name: "cookie fallback",
			header: http.Header{
				"Cookie":        []string{AccessCookieName + "=cookie-token"},
				"Authorization": []string{""},
			},
			wantToken:      "cookie-token",
			wantFromCookie: true,
		},
		{
			name: "header wins over cookie",
			header: http.Header{
				"Cookie":        []string{AccessCookieName + "=cookie-token"},
				"Authorization": []string{"Bearer header-token"},
			},
			wantToken: "header-token",
		},
		{
			name:      "neither carrier",
			header:    http.Header{},
			wantToken: "",
		},
		{
			name:      "non-bearer authorization is ignored",
			header:    http.Header{"Authorization": []string{"Basic dXNlcjpwYXNz"}},
			wantToken: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			token, fromCookie := resolveRequestToken(tc.header)
			assert.Equal(t, tc.wantToken, token)
			assert.Equal(t, tc.wantFromCookie, fromCookie)
			// The header-only helper must stay a strict subset: it never reads
			// cookies, which is what makes the fallback explicit at call sites.
			if tc.wantFromCookie {
				assert.Empty(t, extractToken(tc.header.Get("Authorization")))
			}
		})
	}
}
