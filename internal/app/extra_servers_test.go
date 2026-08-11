package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testTLSServerConfig builds a self-signed TLS config for an ephemeral extra
// listener, mirroring how the orchestrator gateway configures its listener.
func testTLSServerConfig(t *testing.T, host string) *tls.Config {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
}

// TestServeExtraServersLifecycle: an extra listener starts, answers requests,
// reports errors through errCh, and shuts down cleanly.
func TestServeExtraServersLifecycle(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(discardWriter{}, nil))

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	// Rebind: http.Server.ListenAndServe* binds itself; reserve the port by
	// closing the probe listener first (ephemeral port reuse is fine here).
	require.NoError(t, ln.Close())

	extra := &http.Server{
		Addr:      addr,
		TLSConfig: testTLSServerConfig(t, "extra.test"),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}

	errCh := make(chan error, 1)
	serveExtraServers([]*http.Server{extra}, errCh, logger)

	// The listener must accept TLS requests once started.
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}, //nolint:gosec // test-only self-signed cert
		},
		Timeout: 2 * time.Second,
	}
	var resp *http.Response
	require.Eventually(t, func() bool {
		req, err := http.NewRequest(http.MethodGet, "https://"+addr, nil)
		if err != nil {
			return false
		}
		r, err := client.Do(req)
		if err != nil {
			return false
		}
		r.Body.Close()
		resp = r
		return true
	}, 3*time.Second, 50*time.Millisecond)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Graceful shutdown stops the listener.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	shutdownExtraServers(ctx, []*http.Server{extra}, logger)
	req, err := http.NewRequest(http.MethodGet, "https://"+addr, nil)
	require.NoError(t, err)
	r, err := client.Do(req)
	if r != nil {
		r.Body.Close()
	}
	require.Error(t, err, "listener must be closed after shutdown")
	// No unexpected error was reported.
	select {
	case err := <-errCh:
		t.Fatalf("unexpected extra server error: %v", err)
	default:
	}
}

func TestServeExtraServersErrorPropagation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(discardWriter{}, nil))

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	// Second bind on the same port fails: simulate by keeping ln open and
	// pointing the extra server at it — the server's own Listen will fail
	// with address-in-use.
	extra := &http.Server{
		Addr:      ln.Addr().String(),
		TLSConfig: testTLSServerConfig(t, "extra.test"),
		Handler:   http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}),
	}
	errCh := make(chan error, 1)
	serveExtraServers([]*http.Server{extra}, errCh, logger)

	select {
	case err := <-errCh:
		require.Error(t, err)
		require.Contains(t, err.Error(), "extra server")
	case <-time.After(3 * time.Second):
		t.Fatal("expected bind error through errCh")
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

var _ = fmt.Sprintf // keep fmt import for potential debug use
