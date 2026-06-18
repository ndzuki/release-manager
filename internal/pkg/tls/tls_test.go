package tls

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateCA(t *testing.T) {
	cfg := DefaultCAConfig()
	cfg.ValidFor = 24 * time.Hour // 测试用短有效期
	cert, key, err := GenerateCA(cfg)

	require.NoError(t, err)
	require.NotNil(t, cert)
	require.NotNil(t, key)

	assert.True(t, cert.IsCA)
	assert.Equal(t, "release-operator-ca", cert.Subject.CommonName)
	assert.Equal(t, []string{"Release Operator"}, cert.Subject.Organization)
}

func TestGenerateServerCert(t *testing.T) {
	caCert, caKey, err := GenerateCA(DefaultCAConfig())
	require.NoError(t, err)

	cfg := DefaultServerCertConfig()
	cfg.CommonName = "test-server"
	cfg.DNSNames = []string{"localhost", "test-server.local"}

	cert, key, err := GenerateServerCert(caCert, caKey, cfg)
	require.NoError(t, err)
	require.NotNil(t, cert)
	require.NotNil(t, key)

	assert.Equal(t, "test-server", cert.Subject.CommonName)
	assert.Contains(t, cert.DNSNames, "localhost")
	assert.Contains(t, cert.DNSNames, "test-server.local")

	// 验证证书由 CA 签发
	err = cert.CheckSignatureFrom(caCert)
	assert.NoError(t, err)
}

func TestGenerateClientCert(t *testing.T) {
	caCert, caKey, err := GenerateCA(DefaultCAConfig())
	require.NoError(t, err)

	cfg := DefaultClientCertConfig()
	cfg.CommonName = "customer-001"

	cert, key, err := GenerateClientCert(caCert, caKey, cfg)
	require.NoError(t, err)
	require.NotNil(t, cert)
	require.NotNil(t, key)

	assert.Equal(t, "customer-001", cert.Subject.CommonName)
	assert.Equal(t, []string{"Customer"}, cert.Subject.Organization)

	// 验证证书由 CA 签发
	err = cert.CheckSignatureFrom(caCert)
	assert.NoError(t, err)
}

func TestCertFingerprint(t *testing.T) {
	cfg := DefaultCAConfig()
	cert, _, err := GenerateCA(cfg)
	require.NoError(t, err)

	fp := CertFingerprint(cert)
	assert.Len(t, fp, 64) // SHA256 十六进制 64 字符
}

func TestVerifyCertFingerprint(t *testing.T) {
	// 生成两个客户端证书
	caCert, caKey, err := GenerateCA(DefaultCAConfig())
	require.NoError(t, err)

	cfg1 := DefaultClientCertConfig()
	cfg1.CommonName = "customer-001"
	cert1, _, err := GenerateClientCert(caCert, caKey, cfg1)
	require.NoError(t, err)

	fp1 := CertFingerprint(cert1)
	allowed := map[string]bool{fp1: true}

	verifyFn := VerifyCertFingerprint(allowed)

	// cert1 应在白名单中
	err = verifyFn([][]byte{cert1.Raw}, nil)
	assert.NoError(t, err)

	// 不同证书应被拒绝
	cfg2 := DefaultClientCertConfig()
	cfg2.CommonName = "customer-002"
	cert2, _, err := GenerateClientCert(caCert, caKey, cfg2)
	require.NoError(t, err)

	err = verifyFn([][]byte{cert2.Raw}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in the allowed list")
}

func TestVerifyCertFingerprint_NoCert(t *testing.T) {
	verifyFn := VerifyCertFingerprint(map[string]bool{})
	err := verifyFn([][]byte{}, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no client certificate")
}

func TestWriteAndReadPEM(t *testing.T) {
	dir := t.TempDir()

	cfg := DefaultCAConfig()
	cert, key, err := GenerateCA(cfg)
	require.NoError(t, err)

	certPath := filepath.Join(dir, "test.crt")
	keyPath := filepath.Join(dir, "test.key")

	err = WriteCertPEM(certPath, cert)
	require.NoError(t, err)

	err = WriteKeyPEM(keyPath, key)
	require.NoError(t, err)

	// 验证文件存在且可读
	_, err = os.Stat(certPath)
	assert.NoError(t, err)
	_, err = os.Stat(keyPath)
	assert.NoError(t, err)

	// 验证指纹提取
	fp, err := FingerprintFromCertFile(certPath)
	require.NoError(t, err)
	assert.Len(t, fp, 64)
	assert.Equal(t, CertFingerprint(cert), fp)
}

func TestCertPoolFromFile(t *testing.T) {
	dir := t.TempDir()

	cfg := DefaultCAConfig()
	cert, _, err := GenerateCA(cfg)
	require.NoError(t, err)

	caPath := filepath.Join(dir, "ca.crt")
	err = WriteCertPEM(caPath, cert)
	require.NoError(t, err)

	pool, err := CertPoolFromFile(caPath)
	require.NoError(t, err)
	assert.NotNil(t, pool)
}

func TestCertPoolFromFile_Invalid(t *testing.T) {
	_, err := CertPoolFromFile("/nonexistent/ca.crt")
	assert.Error(t, err)
}

func TestLoadClientTLS(t *testing.T) {
	dir := t.TempDir()

	caCert, caKey, err := GenerateCA(DefaultCAConfig())
	require.NoError(t, err)

	clientCfg := DefaultClientCertConfig()
	clientCfg.CommonName = "test-client"
	clientCert, clientKey, err := GenerateClientCert(caCert, caKey, clientCfg)
	require.NoError(t, err)

	certPath := filepath.Join(dir, "client.crt")
	keyPath := filepath.Join(dir, "client.key")
	caPath := filepath.Join(dir, "ca.crt")

	err = WriteCertPEM(certPath, clientCert)
	require.NoError(t, err)
	err = WriteKeyPEM(keyPath, clientKey)
	require.NoError(t, err)
	err = WriteCertPEM(caPath, caCert)
	require.NoError(t, err)

	tlsCfg, err := LoadClientTLS(certPath, keyPath, caPath)
	require.NoError(t, err)
	assert.NotNil(t, tlsCfg)
	assert.Len(t, tlsCfg.Certificates, 1)
	assert.NotNil(t, tlsCfg.RootCAs)
}

func TestLoadClientTLS_NoCA(t *testing.T) {
	dir := t.TempDir()

	caCert, caKey, err := GenerateCA(DefaultCAConfig())
	require.NoError(t, err)

	clientCfg := DefaultClientCertConfig()
	clientCfg.CommonName = "test-client"
	clientCert, clientKey, err := GenerateClientCert(caCert, caKey, clientCfg)
	require.NoError(t, err)

	certPath := filepath.Join(dir, "client.crt")
	keyPath := filepath.Join(dir, "client.key")

	err = WriteCertPEM(certPath, clientCert)
	require.NoError(t, err)
	err = WriteKeyPEM(keyPath, clientKey)
	require.NoError(t, err)

	tlsCfg, err := LoadClientTLS(certPath, keyPath, "")
	require.NoError(t, err)
	assert.NotNil(t, tlsCfg)
	assert.Nil(t, tlsCfg.RootCAs) // 无 CA 验证
}

// Test cross-signing verification: server cert signed by CA should be
// verifiable against the CA cert pool.
func TestServerCertValidation(t *testing.T) {
	caCert, caKey, err := GenerateCA(DefaultCAConfig())
	require.NoError(t, err)

	serverCfg := DefaultServerCertConfig()
	serverCfg.CommonName = "test-server"
	serverCert, _, err := GenerateServerCert(caCert, caKey, serverCfg)
	require.NoError(t, err)

	// 用 CA 构建证书池
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	_, err = serverCert.Verify(x509.VerifyOptions{
		Roots: pool,
	})
	assert.NoError(t, err)
}
