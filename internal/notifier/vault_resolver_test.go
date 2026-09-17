package notifier_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/notifier"
)

func writeToken(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

// writeJSON answers a fake Vault response; a broken test server should fail the
// test rather than silently return an empty body (errcheck check-blank).
func writeJSON(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	_, err := w.Write([]byte(body))
	require.NoError(t, err)
}

func resolverOptions(tokenPath, address string) notifier.VaultResolverOptions {
	return notifier.VaultResolverOptions{
		Address:    address,
		AuthMount:  "kubernetes",
		Role:       "release-notifier",
		TokenPath:  tokenPath,
		KVMount:    "secret",
		SecretPath: "release-manager/webhook",
		SecretKey:  "webhook_secret",
	}
}

// TestNewVaultResolverFailsClosedOnMissingReferences covers ADR-020: an enabled
// resolver with an incomplete reference set must stop startup rather than let
// delivery continue unauthenticated.
func TestNewVaultResolverFailsClosedOnMissingReferences(t *testing.T) {
	complete := resolverOptions("/tmp/token", "http://127.0.0.1:8200")
	cases := map[string]func(*notifier.VaultResolverOptions){
		"address":     func(o *notifier.VaultResolverOptions) { o.Address = "" },
		"auth mount":  func(o *notifier.VaultResolverOptions) { o.AuthMount = "" },
		"role":        func(o *notifier.VaultResolverOptions) { o.Role = "" },
		"token path":  func(o *notifier.VaultResolverOptions) { o.TokenPath = "" },
		"kv mount":    func(o *notifier.VaultResolverOptions) { o.KVMount = "" },
		"secret path": func(o *notifier.VaultResolverOptions) { o.SecretPath = "" },
		"secret key":  func(o *notifier.VaultResolverOptions) { o.SecretKey = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			opts := complete
			mutate(&opts)
			_, err := notifier.NewVaultResolver(opts)
			require.Error(t, err, "a missing reference must fail closed")
			assert.Contains(t, err.Error(), "is required", "the error names the missing reference")
		})
	}
}

// TestVaultResolverAuthenticatesWithKubernetesAndReadsKVv2 is the happy path:
// the resolver exchanges the projected service-account JWT for a Vault token and
// reads the configured KV v2 field with the caller context.
func TestVaultResolverAuthenticatesWithKubernetesAndReadsKVv2(t *testing.T) {
	var loginCalls, secretCalls atomic.Int64
	var sawRole, sawJWT, sawToken atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/kubernetes/login":
			loginCalls.Add(1)
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			sawRole.Store(body["role"])
			sawJWT.Store(body["jwt"])
			writeJSON(t, w, `{"auth":{"client_token":"vault-token","lease_duration":3600,"renewable":true}}`)
		case "/v1/secret/data/release-manager/webhook":
			secretCalls.Add(1)
			sawToken.Store(r.Header.Get("X-Vault-Token"))
			writeJSON(t, w, `{"data":{"data":{"webhook_secret":"s3cr3t-value"},"metadata":{"version":1}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	resolver, err := notifier.NewVaultResolver(resolverOptions(writeToken(t, "sa-jwt\n"), server.URL))
	require.NoError(t, err)
	defer resolver.Close()

	require.NoError(t, resolver.Start(t.Context()))
	assert.EqualValues(t, 1, loginCalls.Load(), "Start must perform the Kubernetes login")
	assert.Equal(t, "release-notifier", sawRole.Load())
	assert.Equal(t, "sa-jwt", sawJWT.Load(), "the projected JWT is trimmed before use")

	value, err := resolver.Resolve(t.Context(), "webhook_secret")
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t-value", value)
	assert.Equal(t, "vault-token", sawToken.Load(), "the KV read must carry the exchanged token")
	assert.EqualValues(t, 1, secretCalls.Load())
}

// TestVaultResolverReportsMissingKeyWithoutLeaking covers ADR-020's error
// contract: a missing path or key is a typed not-found, and neither the error
// nor its text may carry secret material.
func TestVaultResolverReportsMissingKeyWithoutLeaking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/auth/kubernetes/login" {
			writeJSON(t, w, `{"auth":{"client_token":"vault-token","lease_duration":3600}}`)
			return
		}
		writeJSON(t, w, `{"data":{"data":{"another_field":"do-not-leak-me"},"metadata":{"version":2}}}`)
	}))
	defer server.Close()

	resolver, err := notifier.NewVaultResolver(resolverOptions(writeToken(t, "sa-jwt"), server.URL))
	require.NoError(t, err)
	defer resolver.Close()
	require.NoError(t, resolver.Start(t.Context()))

	_, err = resolver.Resolve(t.Context(), "webhook_secret")
	require.Error(t, err)
	assert.ErrorIs(t, err, notifier.ErrSecretNotFound)
	assert.NotContains(t, err.Error(), "do-not-leak-me", "an error must never become the secret's escape route")
}

// TestVaultResolverHonoursCancellation covers the context contract ADR-020
// requires: a cancelled caller must not wait for Vault.
func TestVaultResolverHonoursCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/kubernetes/login" {
			writeJSON(t, w, `{"auth":{"client_token":"vault-token","lease_duration":3600}}`)
			return
		}
		<-release
	}))
	defer server.Close()
	defer close(release)

	resolver, err := notifier.NewVaultResolver(resolverOptions(writeToken(t, "sa-jwt"), server.URL))
	require.NoError(t, err)
	defer resolver.Close()
	require.NoError(t, resolver.Start(t.Context()))

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err = resolver.Resolve(ctx, "webhook_secret")
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled),
		"a cancelled read must surface the context error, got %v", err)
}

// TestVaultResolverRejectsEmptyServiceAccountToken covers the projection edge:
// an empty projected JWT must fail closed instead of authenticating as nobody.
func TestVaultResolverRejectsEmptyServiceAccountToken(t *testing.T) {
	resolver, err := notifier.NewVaultResolver(resolverOptions(writeToken(t, "  \n"), "http://127.0.0.1:8200"))
	require.NoError(t, err)
	defer resolver.Close()

	err = resolver.Start(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
	assert.False(t, strings.Contains(err.Error(), "sa-jwt"))
}
