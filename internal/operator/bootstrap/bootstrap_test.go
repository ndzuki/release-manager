package bootstrap

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/localstore"
)

// stubEnroller records calls and returns a scripted response.
type stubEnroller struct {
	calls atomic.Int32
	resp  *connect.Response[operatorv1.EnrollResponse]
	err   error
}

func (s *stubEnroller) Enroll(_ context.Context, _ *connect.Request[operatorv1.EnrollRequest]) (*connect.Response[operatorv1.EnrollResponse], error) {
	s.calls.Add(1)
	return s.resp, s.err
}

// sequenceEnroller plays back a scripted sequence of results (retry tests).
type sequenceEnroller struct {
	results []seqResult
	used    atomic.Int32
}

type seqResult struct {
	resp *connect.Response[operatorv1.EnrollResponse]
	err  error
}

func (s *sequenceEnroller) Enroll(_ context.Context, _ *connect.Request[operatorv1.EnrollRequest]) (*connect.Response[operatorv1.EnrollResponse], error) {
	idx := int(s.used.Add(1)) - 1
	if idx >= len(s.results) {
		idx = len(s.results) - 1
	}
	return s.results[idx].resp, s.results[idx].err
}

func (s *sequenceEnroller) calls() int { return int(s.used.Load()) }

// recordingEnroller captures the request for assertions.
type recordingEnroller struct {
	onEnroll func(req *operatorv1.EnrollRequest)
}

func (r *recordingEnroller) Enroll(_ context.Context, req *connect.Request[operatorv1.EnrollRequest]) (*connect.Response[operatorv1.EnrollResponse], error) {
	r.onEnroll(req.Msg)
	return enrollResponse(), nil
}

// memIdentityStore is an in-memory IdentityStore for tests.
type memIdentityStore struct {
	identity *localstore.Identity
	saved    int
	err      error
}

func (m *memIdentityStore) SaveIdentity(_ context.Context, identity *localstore.Identity) error {
	if m.err != nil {
		return m.err
	}
	m.identity = identity
	m.saved++
	return nil
}

func (m *memIdentityStore) LoadIdentity(_ context.Context) (*localstore.Identity, error) {
	if m.identity != nil {
		return m.identity, nil
	}
	return nil, localstore.ErrNotFound
}

func enrollResponse() *connect.Response[operatorv1.EnrollResponse] {
	return connect.NewResponse(&operatorv1.EnrollResponse{
		SessionId:      "sess-1",
		TtlSeconds:     600,
		CertificatePem: []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"),
		OperatorId:     "op-center-1",
	})
}

// testConfig returns a config with a real token file and a stub enroller.
func testConfig(t *testing.T, store *memIdentityStore, enroller Enroller) Config {
	t.Helper()
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899\n"), 0o600))
	return Config{
		GatewayURL:    "https://operator-gateway.dev.release-manager.local:30084",
		CAFilePath:    filepath.Join(dir, "ca.crt"),
		TokenFile:     tokenPath,
		CustomerID:    "dev-customer-a",
		ClusterID:     "dev-customer-a-direct",
		OperatorName:  "dev-customer-a-direct",
		IdentityStore: store,
		Enroller:      enroller,
	}
}

func TestBootstrapPersistedIdentitySkipsEnrollment(t *testing.T) {
	store := &memIdentityStore{identity: &localstore.Identity{OperatorID: "op-1", SessionID: "sess-old"}}
	enroller := &stubEnroller{resp: enrollResponse()}
	cfg := testConfig(t, store, enroller)

	// The store returns an identity: no Enroll call must happen (AC-075-02).
	cfg.GatewayURL = "" // would fail if enrollment were attempted
	cfg.CAFilePath = ""
	cfg.Enroller = nil
	result, err := Bootstrap(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, "op-1", result.Identity.OperatorID)
	assert.Equal(t, "sess-old", result.SessionID)
	assert.Zero(t, enroller.calls.Load())
	assert.Zero(t, store.saved)
}

func TestBootstrapEnrollsAndPersistsIdentity(t *testing.T) {
	store := &memIdentityStore{}
	enroller := &stubEnroller{resp: enrollResponse()}
	cfg := testConfig(t, store, enroller)

	result, err := Bootstrap(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "sess-1", result.SessionID)
	assert.Equal(t, 1, store.saved)
	require.NotNil(t, store.identity)
	assert.Equal(t, "dev-customer-a", store.identity.CustomerID)
	assert.Equal(t, "dev-customer-a-direct", store.identity.ClusterID)
	assert.Equal(t, "dev-customer-a-direct", store.identity.OperatorName)
	assert.NotEmpty(t, store.identity.OperatorID)
	assert.Contains(t, store.identity.PrivateKeyPEM, "PRIVATE KEY")
	assert.Contains(t, store.identity.CertificatePEM, "CERTIFICATE")
	// Token file is consumed after durable save (single-use semantics).
	_, err = os.Stat(cfg.TokenFile)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestBootstrapTokenRejectionFailsFast(t *testing.T) {
	store := &memIdentityStore{}
	rejected := connect.NewError(connect.CodeUnauthenticated, errors.New("invalid enrollment token"))
	enroller := &stubEnroller{resp: nil, err: rejected}
	cfg := testConfig(t, store, enroller)

	_, err := Bootstrap(context.Background(), cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenInvalid)
	// Permanent rejection: exactly one attempt, no backoff retry.
	assert.Equal(t, int32(1), enroller.calls.Load())
	assert.Zero(t, store.saved)
}

func TestBootstrapTokenExpiredFailsFast(t *testing.T) {
	store := &memIdentityStore{}
	rejected := connect.NewError(connect.CodeUnauthenticated, errors.New("enrollment token expired"))
	enroller := &stubEnroller{resp: nil, err: rejected}
	cfg := testConfig(t, store, enroller)

	_, err := Bootstrap(context.Background(), cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenInvalid)
	assert.Equal(t, int32(1), enroller.calls.Load())
}

func TestBootstrapTokenReusedFailsFast(t *testing.T) {
	store := &memIdentityStore{}
	rejected := connect.NewError(connect.CodeUnauthenticated, errors.New("enrollment token already used"))
	enroller := &stubEnroller{resp: nil, err: rejected}
	cfg := testConfig(t, store, enroller)

	_, err := Bootstrap(context.Background(), cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTokenInvalid)
	assert.Equal(t, int32(1), enroller.calls.Load())
}

func TestBootstrapTokenFileKeptOnFailureRemovedOnSuccess(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899\n"), 0o600))

	// Failure (token rejection) must keep the file: the agent restarts with
	// the same credential and may retry after the operator fixes the token.
	rejected := connect.NewError(connect.CodeUnauthenticated, errors.New("invalid enrollment token"))
	cfg := Config{
		TokenFile:     tokenPath,
		CustomerID:    "c",
		ClusterID:     "cl",
		OperatorName:  "cl",
		IdentityStore: &memIdentityStore{},
		Enroller:      &stubEnroller{err: rejected},
	}
	_, err := Bootstrap(context.Background(), cfg)
	require.Error(t, err)
	_, statErr := os.Stat(tokenPath)
	require.NoError(t, statErr, "token file must survive a failed enrollment")

	// Success removes the file only after the identity is durable.
	store := &memIdentityStore{}
	cfg = Config{
		TokenFile:     tokenPath,
		CustomerID:    "c",
		ClusterID:     "cl",
		OperatorName:  "cl",
		IdentityStore: store,
		Enroller:      &stubEnroller{resp: enrollResponse()},
	}
	_, err = Bootstrap(context.Background(), cfg)
	require.NoError(t, err)
	_, statErr = os.Stat(tokenPath)
	require.ErrorIs(t, statErr, os.ErrNotExist, "consumed token file must be removed after durable save")
}

func TestBootstrapRetriesTransientError(t *testing.T) {
	store := &memIdentityStore{}
	transient := connect.NewError(connect.CodeUnavailable, errors.New("connect: connection refused"))
	seq := &sequenceEnroller{
		results: []seqResult{
			{resp: nil, err: transient},
			{resp: enrollResponse(), err: nil},
		},
	}
	cfg := testConfig(t, store, seq)

	result, err := Bootstrap(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, "sess-1", result.SessionID)
	assert.Equal(t, 2, seq.calls())
	assert.Equal(t, 1, store.saved)
}

func TestBootstrapPermanentErrorFailsFast(t *testing.T) {
	store := &memIdentityStore{}
	// A scope mismatch (cluster not registered for the customer) is a
	// permanent configuration error: the agent must surface it, not retry
	// forever (REQ-015 error model client handling).
	mismatch := connect.NewError(connect.CodeInvalidArgument, errors.New("cluster does not belong to customer"))
	enroller := &stubEnroller{resp: nil, err: mismatch}
	cfg := testConfig(t, store, enroller)

	_, err := Bootstrap(context.Background(), cfg)
	require.Error(t, err)
	assert.ErrorContains(t, err, "permanent error")
	assert.Equal(t, int32(1), enroller.calls.Load())
	assert.Zero(t, store.saved)
}

func TestBootstrapTokenFileTakesPrecedenceOverEnv(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("file-token\n"), 0o600))
	t.Setenv("ENROLLMENT_TOKEN", "env-token")

	var got *operatorv1.EnrollRequest
	enroller := &recordingEnroller{onEnroll: func(req *operatorv1.EnrollRequest) {
		got = req
	}}
	store := &memIdentityStore{}
	cfg := Config{
		TokenFile:     tokenPath,
		TokenEnv:      "ENROLLMENT_TOKEN",
		CustomerID:    "c",
		ClusterID:     "cl",
		OperatorName:  "cl",
		IdentityStore: store,
		Enroller:      enroller,
	}

	_, err := Bootstrap(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "file-token", got.EnrollmentToken)
}

func TestBootstrapEnvTokenFallback(t *testing.T) {
	t.Setenv("ENROLLMENT_TOKEN", "env-token")
	enroller := &recordingEnroller{onEnroll: func(_ *operatorv1.EnrollRequest) {}}
	store := &memIdentityStore{}
	cfg := Config{
		TokenEnv:      "ENROLLMENT_TOKEN",
		CustomerID:    "c",
		ClusterID:     "cl",
		OperatorName:  "cl",
		IdentityStore: store,
		Enroller:      enroller,
	}

	_, err := Bootstrap(context.Background(), cfg)
	require.NoError(t, err)
	// No token file: env token used; success proves it.
}

func TestBootstrapNoTokenSourceFails(t *testing.T) {
	store := &memIdentityStore{}
	cfg := Config{
		CustomerID:    "c",
		ClusterID:     "cl",
		IdentityStore: store,
	}
	_, err := Bootstrap(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "enrollment token not found")
}

func TestNewKeyAndCSRSANs(t *testing.T) {
	keyPEM, csrPEM, err := newKeyAndCSR("dev-customer-a", "dev-customer-a-direct", "dev-customer-a-direct")
	require.NoError(t, err)
	assert.Contains(t, string(keyPEM), "PRIVATE KEY")

	block, _ := pem.Decode(csrPEM)
	require.NotNil(t, block)
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "dev-customer-a-direct", csr.Subject.CommonName)
	require.Contains(t, csr.DNSNames, "dev-customer-a")
	require.Contains(t, csr.DNSNames, "dev-customer-a-direct")
	require.Contains(t, csr.DNSNames, "dev-customer-a-direct.dev-customer-a.rm")
	assert.Equal(t, csr.PublicKeyAlgorithm, x509.Ed25519)
}

func TestResolveTokenEnvFallback(t *testing.T) {
	t.Setenv("RM_TEST_TOKEN", "tok-123")
	token, err := resolveToken(Config{TokenEnv: "RM_TEST_TOKEN"})
	require.NoError(t, err)
	assert.Equal(t, "tok-123", token)

	_, err = resolveToken(Config{})
	require.Error(t, err)
}
