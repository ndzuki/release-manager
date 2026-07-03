// Package config 提供 release-manager 系统的配置加载和管理。
//
// 支持从 YAML 文件加载配置，并与默认值合并。配置涵盖服务器、日志、
// mTLS、Harbor 仓库、Helm 操作、钉钉通知和数据存储等模块。
package config

import (
	cryptotls "crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	tlsutil "github.com/ndzuki/release-manager/internal/pkg/tls"
)

// Config 是 release-manager 系统的顶层配置。
type Config struct {
	// Server 服务器配置（gRPC 和 HTTP 监听地址、超时等）
	Server   ServerConfig `yaml:"server"`
	// Log 日志配置（级别、格式、输出路径）
	Log      LogConfig    `yaml:"log"`
	// TLS mTLS 证书配置
	TLS      TLSConfig    `yaml:"tls"`
	// Harbor Harbor 仓库连接配置
	Harbor   HarborConfig `yaml:"harbor"`
	// Helm Helm 操作配置（超时、默认 namespace、回滚策略等）
	Helm     HelmConfig   `yaml:"helm"`
	// DingTalk 钉钉机器人通知配置
	DingTalk DingTalkConfig `yaml:"dingtalk"`
	// Store 数据存储配置（SQLite 或 PostgreSQL）
	Store    StoreConfig  `yaml:"store"`
	// NotificationEndpoint release-manager 服务的 gRPC 地址（仅 operator 使用）
	NotificationEndpoint string `yaml:"notification_endpoint"`
	// SMTP 邮件配置（邮箱验证和密码找回）
	SMTP SMTPConfig `yaml:"smtp"`
	// DevMode 开发模式：true 时自动初始化 admin/admin，跳过 SMTP
	DevMode bool `yaml:"dev_mode"`
	// APIKey REST API 认证密钥（仅 manager 使用）
	APIKey string `yaml:"api_key"`
}

// ServerConfig 定义 gRPC 和 HTTP 服务器的监听参数。
type ServerConfig struct {
	GRPCAddr        string        `yaml:"grpc_addr"`        // gRPC 监听地址，默认 :8443
	HTTPAddr        string        `yaml:"http_addr"`        // HTTP 监听地址，默认 :8080
	ReadTimeout     time.Duration `yaml:"read_timeout"`     // HTTP 读超时
	WriteTimeout    time.Duration `yaml:"write_timeout"`    // HTTP 写超时
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"` // 优雅关闭超时
}

// LogConfig 定义日志参数。
type LogConfig struct {
	Level  string `yaml:"level"`  // 日志级别: debug, info, warn, error
	Format string `yaml:"format"` // 输出格式: json, console
	Output string `yaml:"output"` // 输出路径: stdout 或文件路径
}

// TLSConfig 定义 mTLS 双向认证配置。
type TLSConfig struct {
	CAFile              string   `yaml:"ca_file"`               // CA 证书文件路径
	CertFile            string   `yaml:"cert_file"`             // 服务端/客户端证书文件路径
	KeyFile             string   `yaml:"key_file"`              // 私钥文件路径
	RequireClientCert   bool     `yaml:"require_client_cert"`   // 是否要求客户端证书
	AllowedFingerprints []string `yaml:"allowed_fingerprints"`  // 允许的客户端证书 SHA256 指纹白名单
}

// HarborConfig 定义 Harbor OCI 仓库连接参数。
type HarborConfig struct {
	URL                string        `yaml:"url"`                   // Harbor 地址，如 https://harbor.example.com
	Username           string        `yaml:"username"`              // 登录用户名
	Password           string        `yaml:"password"`              // 登录密码
	InsecureSkipVerify bool          `yaml:"insecure_skip_verify"`  // 跳过 TLS 证书验证（仅用于自签名证书）
	Timeout            time.Duration `yaml:"timeout"`               // 连接超时
	WebhookHMACSecret  string        `yaml:"webhook_hmac_secret"`   // Harbor webhook HMAC 签名密钥
	CAFile             string        `yaml:"ca_file"`                // Harbor 自签 CA 证书文件路径 (PEM)
}

// HelmConfig 定义 Helm 操作参数。
type HelmConfig struct {
	UpgradeTimeout   time.Duration `yaml:"upgrade_timeout"`    // upgrade 默认超时，默认 10m
	DefaultNamespace string        `yaml:"default_namespace"`  // 默认 K8s namespace
	MaxHistory       int           `yaml:"max_history"`        // 保留的最大 release 版本数
	Atomic           bool          `yaml:"atomic"`             // 失败时自动回滚（helm --atomic）
	Wait             bool          `yaml:"wait"`               // 等待所有资源就绪
	CreateNamespace  bool          `yaml:"create_namespace"`   // 自动创建 namespace
	CacheDir         string        `yaml:"cache_dir"`         // Helm 缓存目录
}

// DingTalkConfig 定义钉钉机器人通知参数。
type DingTalkConfig struct {
	WebhookURL string `yaml:"webhook_url"` // 钉钉机器人 webhook 地址
	Secret     string `yaml:"secret"`      // HMAC 签名密钥
	Enabled    bool   `yaml:"enabled"`     // 是否启用钉钉通知
}

// SMTPConfig 定义邮件发送参数（用于邮箱验证和密码找回）。
type SMTPConfig struct {
	Host     string `yaml:"host"`     // SMTP 服务器地址
	Port     int    `yaml:"port"`     // SMTP 端口 (587/465)
	Username string `yaml:"username"` // SMTP 登录用户名
	Password string `yaml:"password"` // SMTP 登录密码
	From     string `yaml:"from"`     // 发件人地址
	Enabled  bool   `yaml:"enabled"`  // 是否启用 SMTP
}

// StoreConfig 定义 release-manager 的数据存储参数。
type StoreConfig struct {
	Type string `yaml:"type"` // 存储类型: sqlite, postgres
	DSN  string `yaml:"dsn"`  // 数据库连接字符串
}

// DefaultConfig 返回带有生产级默认值的配置。
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			GRPCAddr:        ":8443",
			HTTPAddr:        ":8080",
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    30 * time.Second,
			ShutdownTimeout: 15 * time.Second,
		},
		Log: LogConfig{
			Level:  "info",
			Format: "json",
		},
		TLS: TLSConfig{
			RequireClientCert: true,
		},
		Harbor: HarborConfig{
			Timeout: 60 * time.Second,
		},
		Helm: HelmConfig{
			UpgradeTimeout:   10 * time.Minute,
			DefaultNamespace: "default",
			MaxHistory:       10,
			Atomic:           true,
			Wait:             true,
			CreateNamespace:  false,
			CacheDir:         "/tmp/helm-cache",
		},
		Store: StoreConfig{
			Type: "sqlite",
			DSN:  "data/release-manager.db",
		},
	}
}

// Load 从 YAML 文件读取配置并与默认值合并，path 为空时返回默认配置。
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()

	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config file %s: %w", path, err)
	}

	return cfg, nil
}

// BuildTLSConfig 构建服务端 mTLS 的 tls.Config。
// 加载服务端证书和私钥，可选配置客户端证书验证和指纹白名单。
func (c *TLSConfig) BuildTLSConfig() (*cryptotls.Config, error) {
	if c.CertFile == "" || c.KeyFile == "" {
		return nil, fmt.Errorf("server certificate and key are required")
	}

	cert, err := cryptotls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load server cert/key: %w", err)
	}

	tlsCfg := &cryptotls.Config{
		Certificates: []cryptotls.Certificate{cert},
		MinVersion:   cryptotls.VersionTLS13,
	}

	if c.RequireClientCert {
		tlsCfg.ClientAuth = cryptotls.RequireAndVerifyClientCert

		if c.CAFile != "" {
			caCert, err := os.ReadFile(c.CAFile)
			if err != nil {
				return nil, fmt.Errorf("read CA cert: %w", err)
			}
			caCertPool := x509.NewCertPool()
			if !caCertPool.AppendCertsFromPEM(caCert) {
				return nil, fmt.Errorf("failed to parse CA certificate")
			}
			tlsCfg.ClientCAs = caCertPool
		}

		// 如果配置了指纹白名单，则对等证书必须匹配其中一个
		if len(c.AllowedFingerprints) > 0 {
			fingerprints := make(map[string]bool, len(c.AllowedFingerprints))
			for _, fp := range c.AllowedFingerprints {
				fingerprints[fp] = true
			}
			tlsCfg.VerifyPeerCertificate = tlsutil.VerifyCertFingerprint(fingerprints)
		}
	}

	return tlsCfg, nil
}

// BuildClientTLSConfig 构建客户端 mTLS 的 tls.Config。
// 加载客户端证书和私钥，可选加载 CA 证书用于验证服务端。
func (c *TLSConfig) BuildClientTLSConfig() (*cryptotls.Config, error) {
	if c.CertFile == "" || c.KeyFile == "" {
		return nil, fmt.Errorf("client certificate and key are required")
	}

	cert, err := cryptotls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load client cert/key: %w", err)
	}

	tlsCfg := &cryptotls.Config{
		Certificates: []cryptotls.Certificate{cert},
		MinVersion:   cryptotls.VersionTLS13,
	}

	if c.CAFile != "" {
		caCert, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA cert: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		tlsCfg.RootCAs = caCertPool
	}

	return tlsCfg, nil
}
