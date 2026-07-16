package audit

import (
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

func TestIsSensitiveField(t *testing.T) {
	tests := []struct {
		name     string
		field    string
		expected bool
	}{
		{"password", "password", true},
		{"passwd", "passwd", true},
		{"token", "token", true},
		{"api_key", "api_key", true},
		{"secret", "secret", true},
		{"private_key", "private_key", true},
		{"username", "username", false},
		{"email", "email", false},
		{"action", "action", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsSensitiveField(tt.field))
		})
	}
}

func TestRedactSensitive(t *testing.T) {
	assert.Equal(t, "****REDACTED****", RedactSensitive("password", "my-secret"))
	assert.Equal(t, "admin", RedactSensitive("username", "admin"))
}
