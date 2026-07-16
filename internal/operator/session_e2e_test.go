package operator_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
	"github.com/ndzuki/release-manager/internal/operator"
	"github.com/ndzuki/release-manager/internal/store"
	sqlitestore "github.com/ndzuki/release-manager/internal/store/sqlite"
)

func TestOperatorSessionMTLS(t *testing.T) {
	st, err := sqlitestore.Open("file::memory:?cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	clientCert, caPool, clientCertPEM, caCert := newMTLSIdentity(t)
	serial := clientCert.Leaf.SerialNumber.String()
	ctx := context.Background()
	require.NoError(t, st.Customers().Create(ctx, &store.Customer{ID: "customer-1", Name: "customer", Slug: "customer"}))
	require.NoError(t, st.Clusters().Create(ctx, &store.Cluster{ID: "cluster-1", Name: "cluster", CustomerID: "customer-1"}))
	require.NoError(t, st.Operators().Create(ctx, &store.Operator{
		ID: "operator-1", CustomerID: "customer-1", ClusterID: "cluster-1", CertSerial: serial,
	}))

	svc, err := operator.NewService(st, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	path, handler := operatorv1connect.NewOperatorServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(path, operator.NewCertificateIdentityHandler(handler))
	server := httptest.NewUnstartedServer(mux)
	server.TLS = &tls.Config{
		MinVersion: tls.VersionTLS13,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  caPool,
	}
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)

	baseTransport, ok := server.Client().Transport.(*http.Transport)
	require.True(t, ok)
	transport := baseTransport.Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      x509.NewCertPool(),
	}
	transport.TLSClientConfig.RootCAs.AddCert(server.Certificate())
	client := operatorv1connect.NewOperatorServiceClient(&http.Client{Transport: transport}, server.URL)
	stream := client.CommandStream(ctx)
	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Hello{Hello: &operatorv1.Hello{
			OperatorId: "operator-1", InstanceId: "instance-1", Version: "1.0.0",
		}},
	}))
	response, err := stream.Receive()
	if err != nil {
		t.Logf("receive error: %T %q", err, err.Error())
	}
	require.NoError(t, err)
	require.NotNil(t, response.GetSessionEstablished())
	assert.NotEmpty(t, response.GetSessionEstablished().GetSessionId())
	assert.Nil(t, response.GetCommand())

	require.NoError(t, stream.Send(&operatorv1.CommandStreamRequest{
		Payload: &operatorv1.CommandStreamRequest_Heartbeat{Heartbeat: &operatorv1.Heartbeat{
			SessionId: response.GetSessionEstablished().GetSessionId(),
		}},
	}))
	require.NoError(t, stream.CloseRequest())

	active, err := st.Sessions().GetActiveByOperator(ctx, "operator-1")
	if err == nil {
		assert.Equal(t, "instance-1", active.InstanceID)
	}
	assert.NotEmpty(t, clientCertPEM)
	assert.True(t, caCert.IsCA)
}

func newMTLSIdentity(t *testing.T) (
	clientCertificate tls.Certificate,
	caPool *x509.CertPool,
	clientPEM []byte,
	caCert *x509.Certificate,
) {
	t.Helper()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	now := time.Now().UTC()
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	require.NoError(t, err)
	caCert, err = x509.ParseCertificate(caDER)
	require.NoError(t, err)

	clientPublic, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "operator"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, clientPublic, caPrivate)
	require.NoError(t, err)
	clientPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER})
	privateBytes, err := x509.MarshalPKCS8PrivateKey(clientPrivate)
	require.NoError(t, err)
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateBytes})
	clientCertificate, err = tls.X509KeyPair(clientPEM, privatePEM)
	require.NoError(t, err)
	clientCertificate.Leaf, err = x509.ParseCertificate(clientDER)
	require.NoError(t, err)

	caPool = x509.NewCertPool()
	caPool.AddCert(caCert)
	return clientCertificate, caPool, clientPEM, caCert
}
