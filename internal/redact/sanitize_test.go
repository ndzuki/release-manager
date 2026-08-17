package redact

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitize_PasswordInJSON(t *testing.T) {
	input := `{"password": "s3cret!", "user": "admin"}`
	result, redacted := Sanitize(input)
	assert.True(t, redacted)
	assert.Contains(t, result, "****REDACTED****")
	assert.NotContains(t, result, "s3cret!")
	assert.Contains(t, result, "user")
}

func TestSanitize_TokenInString(t *testing.T) {
	input := `token=abc123xyz`
	result, redacted := Sanitize(input)
	assert.True(t, redacted)
	assert.Contains(t, result, "****REDACTED****")
	assert.NotContains(t, result, "abc123xyz")
}

func TestSanitize_PrivateKeyPEM(t *testing.T) {
	input := "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC..."
	result, redacted := Sanitize(input)
	assert.True(t, redacted)
	assert.Contains(t, result, "****REDACTED PRIVATE KEY****")
}

func TestSanitize_CertificatePEM(t *testing.T) {
	input := "-----BEGIN CERTIFICATE-----\nMIIC..."
	result, redacted := Sanitize(input)
	assert.True(t, redacted)
	assert.Contains(t, result, "****REDACTED CERTIFICATE****")
}

func TestSanitize_CleanInput(t *testing.T) {
	input := `{"user": "admin", "action": "deploy"}`
	result, redacted := Sanitize(input)
	assert.False(t, redacted)
	assert.Equal(t, input, result)
}

func TestSanitize_APIKey(t *testing.T) {
	input := `api_key: "sk-1234567890abcdef"`
	result, redacted := Sanitize(input)
	assert.True(t, redacted)
	assert.Contains(t, result, "****REDACTED****")
	assert.NotContains(t, result, "sk-1234567890abcdef")
}

func TestSanitize_SQLWithSecretManifest(t *testing.T) {
	input := `helm upgrade failed: INSERT INTO audit (token) VALUES ('s3cret'); ` +
		`secret stringData: {"api_key": "sk-live-123"}`
	result, redacted := Sanitize(input)
	assert.True(t, redacted)
	assert.NotContains(t, result, "s3cret")
	assert.NotContains(t, result, "sk-live-123")
	assert.Contains(t, result, "****REDACTED****")
}

func TestIsSensitiveField(t *testing.T) {
	tests := []struct {
		name     string
		field    string
		expected bool
	}{
		{"password", "password", true},
		{"passwd", "passwd", true},
		{"token", "token", true},
		{"api key", "api_key", true},
		{"private key", "private_key", true},
		{"cert key", "cert_key", true},
		{"credential", "credential", true},
		{"benign", "username", false},
		{"replicas", "replicas", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsSensitiveField(tt.field))
		})
	}
}

func TestRedactSensitive(t *testing.T) {
	assert.Equal(t, "****REDACTED****", RedactSensitive("token", "abc123"))
	assert.Equal(t, "api-v1", RedactSensitive("image_ref", "api-v1"))
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		max      int
		expected string
	}{
		{"short stays", "hello", 10, "hello"},
		{"exact fits", "hello", 5, "hello"},
		{"unicode runes not bytes", "你好世界", 3, "你好世"},
		{"zero max", "hello", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, Truncate(tt.input, tt.max))
		})
	}
	// Truncate must never split a multi-byte rune.
	got := Truncate(strings.Repeat("世", 1000), 10)
	assert.Equal(t, "世世世世世世世...", got)
}
