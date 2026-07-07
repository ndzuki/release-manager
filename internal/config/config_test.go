package config

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GenerateTestCA creates a self-signed CA certificate for testing.
func GenerateTestCA() (certPEM, keyPEM []byte, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(1 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM, nil
}

// GenerateTestServerCert creates a server certificate signed by the test CA.
func GenerateTestServerCert(caCertPEM, caKeyPEM []byte) (certPEM, keyPEM []byte, err error) {
	caBlock, _ := pem.Decode(caCertPEM)
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	caKeyBlock, _ := pem.Decode(caKeyPEM)
	caKey, err := x509.ParsePKCS1PrivateKey(caKeyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-server"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(1 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM, nil
}

// GenerateTestClientCert creates a client certificate signed by the test CA.
func GenerateTestClientCert(caCertPEM, caKeyPEM []byte) (certPEM, keyPEM []byte, err error) {
	caBlock, _ := pem.Decode(caCertPEM)
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	caKeyBlock, _ := pem.Decode(caKeyPEM)
	caKey, err := x509.ParsePKCS1PrivateKey(caKeyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-1 * time.Hour),
		NotAfter:     time.Now().Add(1 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM, nil
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load("")
	require.NoError(t, err)
	assert.NotNil(t, cfg)

	// Verify defaults
	assert.Equal(t, ":8443", cfg.Server.GRPCAddr)
	assert.Equal(t, ":8080", cfg.Server.HTTPAddr)
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.Equal(t, "sqlite", cfg.Store.Type)
	assert.Equal(t, "data/release-manager.db", cfg.Store.DSN)
	assert.True(t, cfg.TLS.RequireClientCert)
	assert.Equal(t, "/var/lib/release-operator/certs", cfg.TLS.HotReloadDir)
	assert.Equal(t, 10*60*int64(1e9), int64(cfg.Helm.UpgradeTimeout)) // 10 minutes
	assert.True(t, cfg.Helm.Atomic)
	assert.True(t, cfg.Helm.Wait)
}

func TestLoad_ValidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	yamlContent := `
server:
  grpc_addr: ":9443"
  http_addr: ":9090"
  read_timeout: 60s
  write_timeout: 60s
  shutdown_timeout: 30s
log:
  level: debug
  format: console
  output: stdout
tls:
  ca_file: /etc/certs/ca.pem
  cert_file: /etc/certs/server.pem
  key_file: /etc/certs/server-key.pem
  require_client_cert: false
  hot_reload_dir: /custom/certs
harbor:
  url: https://harbor.example.com
  username: robot
  password: secret
  insecure_skip_verify: true
  timeout: 30s
  webhook_hmac_secret: hmac-key
helm:
  upgrade_timeout: 5m
  default_namespace: production
  max_history: 5
  atomic: false
  wait: true
store:
  type: postgres
  dsn: postgres://user:pass@localhost:5432/release-manager
dingtalk:
  webhook_url: https://oapi.dingtalk.com/robot/send
  secret: ding-secret
  enabled: true
smtp:
  host: smtp.example.com
  port: 587
  username: ops
  password: mail-pass
  from: ops@example.com
  enabled: true
dev_mode: true
api_key: test-api-key
`
	err := os.WriteFile(configPath, []byte(yamlContent), 0o644)
	require.NoError(t, err)

	cfg, err := Load(configPath)
	require.NoError(t, err)

	// Overridden values
	assert.Equal(t, ":9443", cfg.Server.GRPCAddr)
	assert.Equal(t, ":9090", cfg.Server.HTTPAddr)
	assert.Equal(t, "debug", cfg.Log.Level)
	assert.Equal(t, "console", cfg.Log.Format)
	assert.Equal(t, "/custom/certs", cfg.TLS.HotReloadDir)
	assert.False(t, cfg.TLS.RequireClientCert)
	assert.Equal(t, "postgres", cfg.Store.Type)
	assert.True(t, cfg.DingTalk.Enabled)
	assert.True(t, cfg.SMTP.Enabled)
	assert.Equal(t, int(587), cfg.SMTP.Port)
	assert.True(t, cfg.DevMode)
	assert.Equal(t, "test-api-key", cfg.APIKey)
	assert.Equal(t, "production", cfg.Helm.DefaultNamespace)
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read config file")
}

func TestTLSConfig_BuildTLSConfig(t *testing.T) {
	// Missing cert/key returns error
	tlsCfg := &TLSConfig{CertFile: "", KeyFile: ""}
	_, err := tlsCfg.BuildTLSConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "certificate and key are required")
}

func TestTLSConfig_BuildClientTLSConfig(t *testing.T) {
	// Missing cert/key returns error
	tlsCfg := &TLSConfig{CertFile: "", KeyFile: ""}
	_, err := tlsCfg.BuildClientTLSConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "certificate and key are required")

	// Client insecure skip verify
	tlsCfg = &TLSConfig{
		CertFile:                 "/dev/null",
		KeyFile:                  "/dev/null",
		ClientInsecureSkipVerify: true,
	}
	// Will fail at cert loading stage (invalid cert), but that's expected
	_, err = tlsCfg.BuildClientTLSConfig()
	require.Error(t, err) // invalid cert file
}

func TestTLSConfig_HotReloadDir_Default(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, "/var/lib/release-operator/certs", cfg.TLS.HotReloadDir)
}

func TestStoreConfig_Defaults(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, "sqlite", cfg.Store.Type)
	assert.Equal(t, "data/release-manager.db", cfg.Store.DSN)
}

func TestDefaultConfig_AllFields(t *testing.T) {
	cfg := DefaultConfig()

	// Verify no nil pointers and reasonable defaults
	assert.True(t, cfg.Server.ReadTimeout > 0)
	assert.True(t, cfg.Server.WriteTimeout > 0)
	assert.True(t, cfg.Server.ShutdownTimeout > 0)
	assert.True(t, cfg.Helm.UpgradeTimeout > 0)
	assert.Equal(t, 10, cfg.Helm.MaxHistory)
	assert.Equal(t, "/tmp/helm-cache", cfg.Helm.CacheDir)
	assert.False(t, cfg.Helm.CreateNamespace)
}

func TestLoad_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid.yaml")

	err := os.WriteFile(configPath, []byte("this is not valid yaml: {[}"), 0o644)
	require.NoError(t, err)

	_, err = Load(configPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse config file")
}

func TestTLSConfig_BuildTLSConfigWithCerts(t *testing.T) {
	tmpDir := t.TempDir()

	// Generate a CA and server cert
	caCert, caKey, err := GenerateTestCA()
	require.NoError(t, err)

	serverCert, serverKey, err := GenerateTestServerCert(caCert, caKey)
	require.NoError(t, err)

	caFile := filepath.Join(tmpDir, "ca.pem")
	require.NoError(t, os.WriteFile(caFile, caCert, 0o644))

	certFile := filepath.Join(tmpDir, "server.pem")
	require.NoError(t, os.WriteFile(certFile, serverCert, 0o644))

	keyFile := filepath.Join(tmpDir, "server-key.pem")
	require.NoError(t, os.WriteFile(keyFile, serverKey, 0o644))

	// Build TLS config with CA and client cert requirement
	tlsCfg := &TLSConfig{
		CertFile:          certFile,
		KeyFile:           keyFile,
		CAFile:            caFile,
		RequireClientCert: true,
	}
	cfg, err := tlsCfg.BuildTLSConfig()
	require.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.True(t, len(cfg.Certificates) > 0)
	assert.NotNil(t, cfg.ClientCAs)
}

func TestTLSConfig_BuildTLSConfigWithFingerprints(t *testing.T) {
	tmpDir := t.TempDir()

	caCert, caKey, err := GenerateTestCA()
	require.NoError(t, err)

	serverCert, serverKey, err := GenerateTestServerCert(caCert, caKey)
	require.NoError(t, err)

	certFile := filepath.Join(tmpDir, "server.pem")
	require.NoError(t, os.WriteFile(certFile, serverCert, 0o644))

	keyFile := filepath.Join(tmpDir, "server-key.pem")
	require.NoError(t, os.WriteFile(keyFile, serverKey, 0o644))

	// Build TLS config with fingerprint whitelist (no CA file in this case)
	tlsCfg := &TLSConfig{
		CertFile:          certFile,
		KeyFile:           keyFile,
		RequireClientCert: true,
		AllowedFingerprints: []string{
			"AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99",
		},
	}
	cfg, err := tlsCfg.BuildTLSConfig()
	require.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.NotNil(t, cfg.VerifyPeerCertificate)
}

func TestTLSConfig_BuildClientTLSConfigWithCA(t *testing.T) {
	tmpDir := t.TempDir()

	caCert, caKey, err := GenerateTestCA()
	require.NoError(t, err)

	clientCert, clientKey, err := GenerateTestClientCert(caCert, caKey)
	require.NoError(t, err)

	caFile := filepath.Join(tmpDir, "ca.pem")
	require.NoError(t, os.WriteFile(caFile, caCert, 0o644))

	certFile := filepath.Join(tmpDir, "client.pem")
	require.NoError(t, os.WriteFile(certFile, clientCert, 0o644))

	keyFile := filepath.Join(tmpDir, "client-key.pem")
	require.NoError(t, os.WriteFile(keyFile, clientKey, 0o644))

	tlsCfg := &TLSConfig{
		CertFile: certFile,
		KeyFile:  keyFile,
		CAFile:   caFile,
	}
	cfg, err := tlsCfg.BuildClientTLSConfig()
	require.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.NotNil(t, cfg.RootCAs)
	assert.True(t, len(cfg.Certificates) > 0)
}

func TestTLSConfig_BuildClientTLSConfig_SkipVerify(t *testing.T) {
	tmpDir := t.TempDir()

	caCert, caKey, err := GenerateTestCA()
	require.NoError(t, err)

	clientCert, clientKey, err := GenerateTestClientCert(caCert, caKey)
	require.NoError(t, err)

	certFile := filepath.Join(tmpDir, "client.pem")
	require.NoError(t, os.WriteFile(certFile, clientCert, 0o644))

	keyFile := filepath.Join(tmpDir, "client-key.pem")
	require.NoError(t, os.WriteFile(keyFile, clientKey, 0o644))

	tlsCfg := &TLSConfig{
		CertFile:                 certFile,
		KeyFile:                  keyFile,
		ClientInsecureSkipVerify: true,
	}
	cfg, err := tlsCfg.BuildClientTLSConfig()
	require.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.True(t, cfg.InsecureSkipVerify)
}
