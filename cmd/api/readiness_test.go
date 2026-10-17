package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/handler"
)

// apiReadyzHandler replays app.Run's wiring: the implicit noop check plus
// everything the service contributes after Register (internal/app/app.go Run,
// readinessContributor branch). The /readyz semantics under test are therefore
// the ones the process actually serves, not a hand-built variant.
func apiReadyzHandler(svc *apiSvc) http.Handler {
	checks := map[string]func() error{"noop": func() error { return nil }}
	for name, check := range svc.ReadinessChecks() {
		checks[name] = check
	}
	return handler.Ready(checks)
}

// TestAPIReadyzFailsWhenDatabasePingFails pins TASK-352: before the fix api
// advertised only the noop check, so a PostgreSQL outage still returned 200 and
// kubelet kept the Pod in the Service endpoints while every request 502'd.
func TestAPIReadyzFailsWhenDatabasePingFails(t *testing.T) {
	t.Parallel()

	dbErr := errors.New("dial tcp 10.42.0.9:5432: connect: connection refused")
	pinged := false
	svc := &apiSvc{
		readinessPingTimeout: 50 * time.Millisecond,
		pingDB: func(context.Context) error {
			pinged = true
			return dbErr
		},
	}

	rec := httptest.NewRecorder()
	apiReadyzHandler(svc).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody))

	require.True(t, pinged, "the readiness handler must ping the database, not just noop")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "degraded", body.Status)
	assert.Contains(t, body.Checks, "database")
	assert.Contains(t, body.Checks["database"], "connection refused")
	assert.Equal(t, "ok", body.Checks["noop"], "the noop check stays and keeps reporting ok")
}

// TestAPIReadyzPassesWhenDatabasePingSucceeds is the positive control for the
// same handler: a reachable database must keep the previous 200 contract.
func TestAPIReadyzPassesWhenDatabasePingSucceeds(t *testing.T) {
	t.Parallel()

	svc := &apiSvc{pingDB: func(context.Context) error { return nil }}

	rec := httptest.NewRecorder()
	apiReadyzHandler(svc).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody))

	assert.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "ok", body.Status)
	assert.Equal(t, "ok", body.Checks["database"])
	assert.Equal(t, "ok", body.Checks["noop"])
}

// TestAPIReadinessPingUsesTwoSecondDefault pins the production budget: short
// enough to stay under the probe's timeoutSeconds: 3, so kubelet observes the
// real 503 instead of waiting for its own timeout.
func TestAPIReadinessPingUsesTwoSecondDefault(t *testing.T) {
	t.Parallel()

	var remaining time.Duration
	svc := &apiSvc{pingDB: func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		require.True(t, ok, "the database ping must be bounded by a deadline")
		remaining = time.Until(deadline)
		return nil
	}}

	require.NoError(t, svc.ReadinessChecks()["database"]())
	assert.Greater(t, remaining, 1500*time.Millisecond)
	assert.LessOrEqual(t, remaining, 2*time.Second)
}

// TestAPIReadinessPingTimeout proves a hung ping turns into a failed check
// instead of blocking /readyz forever (which would make kubelet time the probe
// out and mask the dependency failure).
func TestAPIReadinessPingTimeout(t *testing.T) {
	t.Parallel()

	svc := &apiSvc{
		readinessPingTimeout: 25 * time.Millisecond,
		pingDB: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}

	check := svc.ReadinessChecks()["database"]
	require.NotNil(t, check)
	start := time.Now()
	err := check()
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), time.Second)
}

// TestAPIReadinessSkipsDatabaseWithoutStore keeps the pre-Register state honest:
// the service advertises no checks rather than a check that cannot run, and a
// nil ping never panics.
func TestAPIReadinessSkipsDatabaseWithoutStore(t *testing.T) {
	t.Parallel()

	svc := &apiSvc{}
	assert.Nil(t, svc.ReadinessChecks())
	rec := httptest.NewRecorder()
	apiReadyzHandler(svc).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestRegisterWiresStorePing pins the wiring: Register must hand the opened
// engine's ping to the readiness check, and a closed pool must fail readiness.
func TestRegisterWiresStorePing(t *testing.T) {
	t.Parallel()

	svc := &apiSvc{dbPath: filepath.Join(t.TempDir(), "api.db"), jwtPublicKey: apiTestJWTPublicKeyPEM}
	require.NoError(t, svc.Register(http.NewServeMux(), slog.New(slog.DiscardHandler)))
	require.NotNil(t, svc.pingDB, "Register must wire the engine ping for /readyz")

	check := svc.ReadinessChecks()["database"]
	require.NotNil(t, check)
	require.NoError(t, check())

	require.NoError(t, svc.Close())
	require.Error(t, check(), "a closed database must fail readiness instead of reporting ok")
}
