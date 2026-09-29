package audit

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"github.com/ndzuki/release-manager/internal/jwtauth"
)

// BrowserSessionCarrier names the browser-session cookies and the CSRF header
// this gate honours. cmd/api passes internal/auth's constants so the names stay
// single-sourced (ADR-028).
type BrowserSessionCarrier struct {
	AccessCookie string
	CSRFCookie   string
	CSRFHeader   string
}

// auditWriteProcedures are the AuditService methods that mutate state. A
// cookie-authenticated call to one of them must also present the CSRF
// double-submit token: accepting the cookie carrier without that rule let a
// cookie session POST Emit with no CSRF proof at all (found by the independent
// review of TASK-174). Bearer callers are exempt — a third-party page cannot
// make them send our cookie.
var auditWriteProcedures = map[string]bool{
	"/audit.v1.AuditService/Emit":              true,
	"/audit.v1.AuditService/ExportAuditEvents": true,
}

type principalContextKey struct{}

// Principal is the authenticated identity used by audit authorization.
type Principal struct {
	UserID string
	Roles  []string
	OrgID  string
	// Authorization is the caller's own bearer credential, forwarded verbatim to
	// release-auth so the authorization decision is made about the caller rather
	// than about this service (ADR-021).
	Authorization string
}

// NewJWTInterceptor validates access tokens and injects the audit principal.
//
// carrier names the browser-session cookies release-auth issues (ADR-028). A
// non-empty AccessCookie is accepted as a credential carrier exactly like the
// Authorization header: the web console authenticates with cookies and sends no
// bearer, so a bearer-only gate answered `missing authorization header` to every
// console audit read even after the entry was routed correctly (B5). Write
// procedures additionally require the CSRF double-submit token when the
// credential came from the cookie. Pass a zero carrier to keep the strict
// bearer-only behaviour.
func NewJWTInterceptor(jwt *jwtauth.Manager, carrier BrowserSessionCarrier) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			value := req.Header().Get("Authorization")
			token, ok := strings.CutPrefix(value, "Bearer ")
			fromCookie := false
			if !ok || token == "" {
				if token = cookieValue(req.Header(), carrier.AccessCookie); token != "" {
					// Forward a real bearer downstream: ADR-021 asks release-auth
					// to decide about THIS caller, and an empty header would make
					// that decision unauthenticated.
					value = "Bearer " + token
					fromCookie = true
				}
			}
			if token == "" {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing authorization header"))
			}

			if fromCookie && auditWriteProcedures[req.Spec().Procedure] {
				if err := validateCSRF(req.Header(), carrier); err != nil {
					return nil, err
				}
			}

			claims, err := jwt.ValidateAccessToken(token)
			if err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid token"))
			}
			ctx = context.WithValue(ctx, principalContextKey{}, Principal{
				UserID:        claims.UserID,
				Roles:         claims.Roles,
				OrgID:         claims.OrgID,
				Authorization: value,
			})
			return next(ctx, req)
		}
	}
}

// validateCSRF enforces the double-submit rule for cookie-authenticated writes:
// the rm_csrf cookie and the X-CSRF-Token header must be present and equal.
func validateCSRF(header http.Header, carrier BrowserSessionCarrier) error {
	cookieToken := cookieValue(header, carrier.CSRFCookie)
	headerToken := header.Get(carrier.CSRFHeader)
	if cookieToken == "" || headerToken == "" || subtle.ConstantTimeCompare([]byte(cookieToken), []byte(headerToken)) != 1 {
		return connect.NewError(connect.CodePermissionDenied, errors.New("csrf token mismatch"))
	}
	return nil
}

// cookieValue returns the named cookie from the request headers, or "" when the
// name is empty or the cookie is absent.
func cookieValue(header http.Header, name string) string {
	if name == "" {
		return ""
	}
	cookie, err := (&http.Request{Header: header}).Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// PrincipalFromContext returns the authenticated audit principal.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}
