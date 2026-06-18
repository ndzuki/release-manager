// Package tls 提供 mTLS 证书生成、验证和 PEM 文件读写工具。
package tls

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"

	"crypto/tls"
)

// CertFingerprint 计算 X.509 证书的 SHA256 指纹。
func CertFingerprint(cert *x509.Certificate) string {
	hash := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(hash[:])
}

// VerifyCertFingerprint 返回用于 tls.Config.VerifyPeerCertificate 的验证函数。
// 验证对等证书的 SHA256 指纹是否在允许列表中。
func VerifyCertFingerprint(allowed map[string]bool) func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("no client certificate provided")
		}

		cert, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("parse client certificate: %w", err)
		}

		fingerprint := CertFingerprint(cert)
		if !allowed[fingerprint] {
			return fmt.Errorf("client certificate fingerprint %s is not in the allowed list", fingerprint)
		}

		return nil
	}
}

// CAConfig 定义 CA 证书的配置参数。
type CAConfig struct {
	// Organization CA 证书的组织名
	Organization string
	// CommonName CA 证书的通用名
	CommonName string
	// ValidFor CA 证书有效期
	ValidFor time.Duration
	// RSAKeyBits RSA 密钥位数
	RSAKeyBits int
}

// DefaultCAConfig 返回默认的 CA 配置（10 年有效期）。
func DefaultCAConfig() CAConfig {
	return CAConfig{
		Organization: "Release Operator",
		CommonName:   "release-operator-ca",
		ValidFor:     10 * 365 * 24 * time.Hour, // 10 年
		RSAKeyBits:   4096,
	}
}

// GenerateCA 生成新的 CA 证书和 RSA 私钥。
func GenerateCA(cfg CAConfig) (*x509.Certificate, *rsa.PrivateKey, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, cfg.RSAKeyBits)
	if err != nil {
		return nil, nil, fmt.Errorf("generate CA private key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{cfg.Organization},
			CommonName:   cfg.CommonName,
		},
		NotBefore:             now,
		NotAfter:              now.Add(cfg.ValidFor),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create CA certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA certificate: %w", err)
	}

	return cert, privateKey, nil
}

// ServerCertConfig 定义服务端证书配置。
type ServerCertConfig struct {
	// Organization 组织名
	Organization string
	// CommonName 服务端证书通用名（应为 hostname）
	CommonName string
	// DNSNames SAN 扩展的 DNS 名称列表
	DNSNames []string
	// IPAddresses SAN 扩展的 IP 地址列表
	IPAddresses []net.IP
	// ValidFor 证书有效期
	ValidFor time.Duration
	// RSAKeyBits RSA 密钥位数
	RSAKeyBits int
}

// DefaultServerCertConfig 返回默认服务端证书配置（3 年有效期）。
func DefaultServerCertConfig() ServerCertConfig {
	return ServerCertConfig{
		Organization: "Release Operator",
		CommonName:   "release-manager",
		DNSNames:     []string{"localhost", "release-manager"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		ValidFor:     3 * 365 * 24 * time.Hour, // 3 年，运维友好
		RSAKeyBits:   2048,
	}
}

// GenerateServerCert 生成由 CA 签发的服务端证书。
func GenerateServerCert(caCert *x509.Certificate, caKey *rsa.PrivateKey, cfg ServerCertConfig) (*x509.Certificate, *rsa.PrivateKey, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, cfg.RSAKeyBits)
	if err != nil {
		return nil, nil, fmt.Errorf("generate server private key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{cfg.Organization},
			CommonName:   cfg.CommonName,
		},
		NotBefore:   now,
		NotAfter:    now.Add(cfg.ValidFor),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:    cfg.DNSNames,
		IPAddresses: cfg.IPAddresses,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &privateKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create server certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, fmt.Errorf("parse server certificate: %w", err)
	}

	return cert, privateKey, nil
}

// ClientCertConfig 定义客户端证书配置。
type ClientCertConfig struct {
	// Organization 组织名
	Organization string
	// CommonName 客户端证书通用名，标识客户，如 "customer-001"
	CommonName string
	// ValidFor 证书有效期
	ValidFor time.Duration
	// RSAKeyBits RSA 密钥位数
	RSAKeyBits int
}

// DefaultClientCertConfig 返回默认客户端证书配置（3 年有效期）。
func DefaultClientCertConfig() ClientCertConfig {
	return ClientCertConfig{
		Organization: "Customer",
		CommonName:   "customer-001",
		ValidFor:     3 * 365 * 24 * time.Hour, // 3 年，运维友好
		RSAKeyBits:   2048,
	}
}

// GenerateClientCert 生成由 CA 签发的客户端证书。
func GenerateClientCert(caCert *x509.Certificate, caKey *rsa.PrivateKey, cfg ClientCertConfig) (*x509.Certificate, *rsa.PrivateKey, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, cfg.RSAKeyBits)
	if err != nil {
		return nil, nil, fmt.Errorf("generate client private key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{cfg.Organization},
			CommonName:   cfg.CommonName,
		},
		NotBefore:   now,
		NotAfter:    now.Add(cfg.ValidFor),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &privateKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create client certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, fmt.Errorf("parse client certificate: %w", err)
	}

	return cert, privateKey, nil
}

// WriteCertPEM 将 x509 证书以 PEM 格式写入文件。
func WriteCertPEM(path string, cert *x509.Certificate) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create cert file %s: %w", path, err)
	}
	defer f.Close()

	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}); err != nil {
		return fmt.Errorf("encode cert PEM: %w", err)
	}

	return nil
}

// WriteKeyPEM 将私钥以 PEM 格式写入文件。
func WriteKeyPEM(path string, key crypto.PrivateKey) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create key file %s: %w", path, err)
	}
	defer f.Close()

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}

	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}); err != nil {
		return fmt.Errorf("encode key PEM: %w", err)
	}

	return nil
}

// CertPoolFromFile 从文件加载 CA 证书并返回 CertPool。
func CertPoolFromFile(caFile string) (*x509.CertPool, error) {
	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate from %s", caFile)
	}

	return pool, nil
}

// FingerprintFromCertFile 读取证书文件并返回其 SHA256 指纹。
func FingerprintFromCertFile(certFile string) (string, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return "", fmt.Errorf("read cert file: %w", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM from %s", certFile)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse certificate: %w", err)
	}

	return CertFingerprint(cert), nil
}

// LoadClientTLS 从证书和私钥文件加载客户端 TLS 配置。
func LoadClientTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load client cert/key: %w", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	if caFile != "" {
		pool, err := CertPoolFromFile(caFile)
		if err != nil {
			return nil, err
		}
		tlsCfg.RootCAs = pool
	}

	return tlsCfg, nil
}
