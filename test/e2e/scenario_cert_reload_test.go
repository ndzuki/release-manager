//go:build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	releasev1 "github.com/ndzuki/release-manager/api/gen/release/v1"
)

// dialWithCert creates an mTLS gRPC connection with the given CA and client certs.
// caFile is the CA certificate PEM used to verify the server.
// certFile and keyFile are the client certificate and key PEM used for client auth.
func dialWithCert(ctx context.Context, addr, caFile, certFile, keyFile string) (*grpc.ClientConn, error) {
	// Load CA certificate to verify the server
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}
	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("failed to parse CA certificate from %s", caFile)
	}

	// Load client certificate
	clientCert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load client key pair: %w", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      certPool,
		MinVersion:   tls.VersionTLS12,
		ServerName:   "", // server certificate CN is used for verification
	}

	creds := credentials.NewTLS(tlsCfg)
	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(creds),
		grpc.WithBlock(),
		grpc.WithReturnConnectionError(),
	)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	return conn, nil
}

// TestCertificateHotReload verifies the operator's mTLS certificate hot reload capability.
//
// Flow:
//  1. Connect to operator with cert-A via mTLS gRPC, call GetOperatorStatus — must succeed
//  2. Generate cert-B via generateCerts with a unique customer ID
//  3. Read cert-B PEM data (cert, key, and CA)
//  4. Call UpdateCertificate RPC with cert-B (TlsCertPem, TlsKeyPem, CaCertPem, RequestId)
//  5. Sleep 3 seconds for the hot reload to take effect
//  6. Connect to operator with cert-B — call GetOperatorStatus — must succeed
//  7. Connect to operator with cert-A — must FAIL (old cert rejected)
func TestCertificateHotReload(t *testing.T) {
	t.Skip("cert reload test requires port-forward redesign — TLS SNI mismatch with localhost")
	h := SetupTest(t)
	defer h.DumpState()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// The operator is deployed via Helm with a ClusterIP service.
	// We use kubectl port-forward to make its gRPC endpoint accessible locally.
	operatorNS := fmt.Sprintf("release-operator-%s", h.CustomerID)
	operatorSvc := fmt.Sprintf("release-operator-%s", h.CustomerID)
	localPort := "18443"
	pfAddr := fmt.Sprintf("localhost:%s", localPort)

	t.Logf("Starting port-forward to operator service %s/%s...", operatorNS, operatorSvc)
	pfCtx, pfCancel := context.WithCancel(context.Background())
	defer pfCancel()

	pfCmd := exec.CommandContext(pfCtx, "kubectl", "port-forward", "-n", operatorNS,
		fmt.Sprintf("svc/%s", operatorSvc), fmt.Sprintf("%s:8443", localPort))

	// Capture stderr for diagnostics (stdout only has forwarding messages on some versions)
	pfStderr, err := pfCmd.StderrPipe()
	require.NoError(t, err)

	if err := pfCmd.Start(); err != nil {
		t.Fatalf("start kubectl port-forward: %v", err)
	}

	// Wait for port-forward to be ready (TCP connectable)
	require.NoError(t, waitForGRPCReady(ctx, pfAddr, 30*time.Second),
		"port-forward should become ready within 30s")
	t.Logf("Port-forward ready at %s", pfAddr)

	// Cleanup: kill port-forward
	defer func() {
		pfCancel()
		_ = pfCmd.Wait()
	}()

	// Read port-forward stderr in background for diagnostics
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := pfStderr.Read(buf)
			if n > 0 {
				t.Logf("[pf-stderr] %s", string(buf[:n]))
			}
			if err != nil {
				return
			}
		}
	}()

	// -----------------------------------------------------------------------
	// Step 1: Connect with cert-A (initial certs from harness) and verify
	//         the operator responds to GetOperatorStatus.
	// -----------------------------------------------------------------------
	t.Log("Step 1: Connecting with cert-A and calling GetOperatorStatus...")
	connA, err := dialWithCert(ctx, pfAddr, h.caFile, h.certFile, h.keyFile)
	require.NoError(t, err, "should establish mTLS connection with cert-A")
	defer connA.Close()

	opClientA := releasev1.NewOperatorServiceClient(connA)
	statusResp, err := opClientA.GetOperatorStatus(ctx, &releasev1.GetOperatorStatusRequest{})
	require.NoError(t, err, "GetOperatorStatus should succeed with cert-A")
	t.Logf("Operator status (cert-A): version=%s  customer=%s  uptime=%ds",
		statusResp.Version, statusResp.CustomerId, statusResp.UptimeSeconds)

	// -----------------------------------------------------------------------
	// Step 2: Generate cert-B with a fresh CA + client cert.
	// -----------------------------------------------------------------------
	t.Log("Step 2: Generating cert-B...")
	caBFile, certBFile, keyBFile, fpB, err := generateCerts("cert-reload-test")
	require.NoError(t, err, "should generate cert-B")

	// All cert-B files live in a single temp directory created by generateCerts.
	certBDir := filepath.Dir(caBFile)
	defer func() {
		t.Logf("Cleaning up cert-B temp directory: %s", certBDir)
		os.RemoveAll(certBDir)
	}()

	t.Logf("cert-B generated: fingerprint=%s", fpB)

	// -----------------------------------------------------------------------
	// Step 3: Read cert-B PEM data (cert, key, and CA).
	// -----------------------------------------------------------------------
	t.Log("Step 3: Reading cert-B PEM data...")
	certBPEM, err := os.ReadFile(certBFile)
	require.NoError(t, err, "read cert-B PEM")
	keyBPEM, err := os.ReadFile(keyBFile)
	require.NoError(t, err, "read cert-B key")
	caBPEM, err := os.ReadFile(caBFile)
	require.NoError(t, err, "read cert-B CA")

	// -----------------------------------------------------------------------
	// Step 4: Call UpdateCertificate RPC with cert-B.
	//
	// We send the new server certificate (TlsCertPem, TlsKeyPem) and the
	// new CA certificate (CaCertPem) so that the operator's TLS config
	// switches to trusting cert-B's CA for client verification as well.
	// -----------------------------------------------------------------------
	t.Log("Step 4: Calling UpdateCertificate RPC with cert-B...")
	notifyClient := releasev1.NewReleaseNotificationServiceClient(connA)
	updateResp, err := notifyClient.UpdateCertificate(ctx, &releasev1.UpdateCertificateRequest{
		TlsCertPem: string(certBPEM),
		TlsKeyPem:  string(keyBPEM),
		CaCertPem:  string(caBPEM),
		RequestId:  uuid.New().String(),
	})
	require.NoError(t, err, "UpdateCertificate RPC should succeed")
	t.Logf("UpdateCertificate response: accepted=%v  message=%q  new_fingerprint=%s",
		updateResp.Accepted, updateResp.Message, updateResp.NewFingerprint)
	require.True(t, updateResp.Accepted, "cert update should be accepted")

	// -----------------------------------------------------------------------
	// Step 5: Wait for hot reload.
	//
	// The operator uses GetCertificate and GetConfigForClient callbacks that
	// read from the filesystem on every TLS handshake. The new certs were
	// written to disk in Step 4; subsequent handshakes will pick them up.
	// We sleep to allow any in-flight handshakes to settle.
	// -----------------------------------------------------------------------
	t.Log("Step 5: Sleeping 3 seconds for hot reload...")
	time.Sleep(3 * time.Second)

	// -----------------------------------------------------------------------
	// Step 6: Connect with cert-B and verify GetOperatorStatus succeeds.
	// -----------------------------------------------------------------------
	t.Log("Step 6: Connecting with cert-B and calling GetOperatorStatus...")
	connB, err := dialWithCert(ctx, pfAddr, caBFile, certBFile, keyBFile)
	require.NoError(t, err, "should establish mTLS connection with cert-B after hot reload")
	defer connB.Close()

	opClientB := releasev1.NewOperatorServiceClient(connB)
	statusRespB, err := opClientB.GetOperatorStatus(ctx, &releasev1.GetOperatorStatusRequest{})
	require.NoError(t, err, "GetOperatorStatus should succeed with cert-B after hot reload")
	t.Logf("Operator status (cert-B): version=%s  customer=%s  uptime=%ds",
		statusRespB.Version, statusRespB.CustomerId, statusRespB.UptimeSeconds)

	// -----------------------------------------------------------------------
	// Step 7: Connect with cert-A — must FAIL because the operator's TLS
	//         config now uses CA-B to verify client certs, and cert-A is
	//         signed by CA-A (not CA-B).
	// -----------------------------------------------------------------------
	t.Log("Step 7: Connecting with cert-A (expecting failure)...")
	_, err = dialWithCert(ctx, pfAddr, h.caFile, h.certFile, h.keyFile)
	require.Error(t, err, "cert-A should be rejected after hot reload to cert-B")
	t.Logf("cert-A correctly rejected: %v", err)
}
