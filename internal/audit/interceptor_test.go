package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	auditv1 "github.com/ndzuki/release-manager/api/gen/audit/v1"
	auditv1connect "github.com/ndzuki/release-manager/api/gen/audit/v1/auditv1connect"
	"github.com/ndzuki/release-manager/internal/jwtauth"
)

// The names mirror release-auth's constants (internal/auth AccessCookieName /
// CSRFCookieName / CSRFHeaderName). cmd/api passes those in; the test uses the
// literals so it fails loudly if the wiring ever stops using a cookie at all.
const (
	testAccessCookieName = "rm_access"
	testCSRFCookieName   = "rm_csrf"
	testCSRFHeaderName   = "X-CSRF-Token"
)

func testCarrier() BrowserSessionCarrier {
	return BrowserSessionCarrier{
		AccessCookie: testAccessCookieName,
		CSRFCookie:   testCSRFCookieName,
		CSRFHeader:   testCSRFHeaderName,
	}
}

func signedAccessToken(t *testing.T) (publicKey ed25519.PublicKey, token string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	token, err = jwt.NewWithClaims(jwt.SigningMethodEdDSA, &jwtauth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
		UserID:           "user-1",
		Roles:            []string{"release_admin"},
		OrgID:            "org-1",
	}).SignedString(privateKey)
	require.NoError(t, err)
	return publicKey, token
}

// capturePrincipal returns an interceptor plus a pointer to the principal the
// wrapped handler observed, so a test can assert what was injected.
func capturePrincipal(t *testing.T, carrier BrowserSessionCarrier) (connect.UnaryFunc, string, *Principal) {
	t.Helper()
	publicKey, token := signedAccessToken(t)
	captured := &Principal{}
	interceptor := NewJWTInterceptor(jwtauth.New(publicKey, time.Hour), carrier)
	wrapped := interceptor(func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		if principal, ok := PrincipalFromContext(ctx); ok {
			*captured = principal
		}
		return connect.NewResponse(&auditv1.QueryAuditEventsResponse{}), nil
	})
	return wrapped, token, captured
}

// TestJWTInterceptorAcceptsBrowserCookie pins the ADR-028 carrier rule for the
// audit surface: the console sends no Authorization header, so a bearer-only gate
// answered `missing authorization header` to every audit read (B5 follow-up).
// The cookie must also be forwarded downstream as a real bearer, because
// ADR-021 asks release-auth to decide about this caller.
func TestJWTInterceptorAcceptsBrowserCookie(t *testing.T) {
	wrapped, token, captured := capturePrincipal(t, testCarrier())

	req := connect.NewRequest(&auditv1.QueryAuditEventsRequest{})
	req.Header().Set("Cookie", testAccessCookieName+"="+token)
	_, err := wrapped(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "user-1", captured.UserID)
	assert.Equal(t, "org-1", captured.OrgID)
	assert.Equal(t, "Bearer "+token, captured.Authorization)
}

// TestJWTInterceptorKeepsBearerCarrier covers the non-browser callers that must
// keep working unchanged.
func TestJWTInterceptorKeepsBearerCarrier(t *testing.T) {
	wrapped, token, captured := capturePrincipal(t, testCarrier())

	req := connect.NewRequest(&auditv1.QueryAuditEventsRequest{})
	req.Header().Set("Authorization", "Bearer "+token)
	_, err := wrapped(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "Bearer "+token, captured.Authorization)
}

// TestJWTInterceptorRejectsMissingCredential keeps the fail-closed shape and
// pins the neutral message: the console used to render the old internal wording
// verbatim.
func TestJWTInterceptorRejectsMissingCredential(t *testing.T) {
	wrapped, _, _ := capturePrincipal(t, testCarrier())

	_, err := wrapped(context.Background(), connect.NewRequest(&auditv1.QueryAuditEventsRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	// The wording is asserted verbatim by test/e2e/prerequisite/smoke.sh, so it
	// stays "missing authorization header" (the 401 itself is the contract).
	assert.ErrorContains(t, err, "missing authorization header")
}

// TestJWTInterceptorStrictWithoutCookieName documents the opt-out: an empty
// cookie name restores bearer-only enforcement.
func TestJWTInterceptorStrictWithoutCookieName(t *testing.T) {
	wrapped, token, _ := capturePrincipal(t, BrowserSessionCarrier{})

	req := connect.NewRequest(&auditv1.QueryAuditEventsRequest{})
	req.Header().Set("Cookie", testAccessCookieName+"="+token)
	_, err := wrapped(context.Background(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// stubAuditHandler records whether the wrapped handler ran; the remaining
// AuditService methods are unused by these interceptor tests.
type stubAuditHandler struct {
	emitted bool
}

func (h *stubAuditHandler) Emit(context.Context, *connect.Request[auditv1.EmitAuditRequest]) (*connect.Response[auditv1.EmitAuditResponse], error) {
	h.emitted = true
	return connect.NewResponse(&auditv1.EmitAuditResponse{}), nil
}

func (h *stubAuditHandler) QueryAuditEvents(context.Context, *connect.Request[auditv1.QueryAuditEventsRequest]) (*connect.Response[auditv1.QueryAuditEventsResponse], error) {
	return connect.NewResponse(&auditv1.QueryAuditEventsResponse{}), nil
}

func (h *stubAuditHandler) ExportAuditEvents(context.Context, *connect.Request[auditv1.ExportAuditEventsRequest]) (*connect.Response[auditv1.ExportAuditEventsResponse], error) {
	return connect.NewResponse(&auditv1.ExportAuditEventsResponse{}), nil
}

// TestJWTInterceptorRequiresCSRFForCookieWrites pins the rule the independent
// review of TASK-174 found missing: adding the cookie carrier without a CSRF
// gate let a cookie session POST Emit with no CSRF proof at all. A write RPC
// authenticated by cookie must present the double-submit token; a read RPC and a
// bearer caller are exempt.
//
// This goes through a real Connect handler on purpose: req.Spec().Procedure is
// only populated on the wire, so a hand-built connect.Request cannot exercise
// the write-procedure branch.
func TestJWTInterceptorRequiresCSRFForCookieWrites(t *testing.T) {
	publicKey, token := signedAccessToken(t)
	handler := &stubAuditHandler{}
	mux := http.NewServeMux()
	path, rpcHandler := auditv1connect.NewAuditServiceHandler(
		handler,
		connect.WithInterceptors(NewJWTInterceptor(jwtauth.New(publicKey, time.Hour), testCarrier())),
	)
	mux.Handle(path, rpcHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := auditv1connect.NewAuditServiceClient(server.Client(), server.URL)
	ctx := context.Background()

	emitWith := func(setup func(*connect.Request[auditv1.EmitAuditRequest])) error {
		req := connect.NewRequest(&auditv1.EmitAuditRequest{})
		setup(req)
		_, err := client.Emit(ctx, req)
		return err
	}
	writeCookie := func(value string) func(*connect.Request[auditv1.EmitAuditRequest]) {
		return func(req *connect.Request[auditv1.EmitAuditRequest]) {
			req.Header().Set("Cookie", value)
		}
	}

	// Cookie + write + no CSRF token → permission_denied.
	err := emitWith(writeCookie(testAccessCookieName + "=" + token))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.ErrorContains(t, err, "csrf token mismatch")
	assert.False(t, handler.emitted)

	// Cookie + write + mismatched token → still refused.
	err = emitWith(func(req *connect.Request[auditv1.EmitAuditRequest]) {
		req.Header().Set("Cookie", testAccessCookieName+"="+token+"; "+testCSRFCookieName+"=csrf-a")
		req.Header().Set(testCSRFHeaderName, "csrf-b")
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.False(t, handler.emitted)

	// Cookie + write + matching token → accepted.
	err = emitWith(func(req *connect.Request[auditv1.EmitAuditRequest]) {
		req.Header().Set("Cookie", testAccessCookieName+"="+token+"; "+testCSRFCookieName+"=csrf-a")
		req.Header().Set(testCSRFHeaderName, "csrf-a")
	})
	require.NoError(t, err)
	assert.True(t, handler.emitted)

	// Cookie + READ procedure + no CSRF token → accepted (the console reads this way).
	read := connect.NewRequest(&auditv1.QueryAuditEventsRequest{})
	read.Header().Set("Cookie", testAccessCookieName+"="+token)
	_, err = client.QueryAuditEvents(ctx, read)
	require.NoError(t, err)

	// Bearer + write + no CSRF token → accepted (a third-party page cannot drive it).
	bearer := connect.NewRequest(&auditv1.EmitAuditRequest{})
	bearer.Header().Set("Authorization", "Bearer "+token)
	_, err = client.Emit(ctx, bearer)
	require.NoError(t, err)
}
