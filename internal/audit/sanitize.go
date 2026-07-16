package audit

import (
	"regexp"
	"strings"
)

// sensitivePatterns defines regex patterns for sensitive field detection.
// AC-050-02: Sensitive fields (password/token/Secret/certificate private key)
// MUST be rejected or redacted.
var sensitivePatterns = []struct {
	pattern    *regexp.Regexp
	redactWith string
}{
	{regexp.MustCompile(`(?i)["']?((?:password|passwd|pwd|secret|token|api_key|apikey|private_key|privkey|cert_key|tls_key))["']?\s*[:=]\s*["']?[^"'\s,}]+["']?`), "${1}=****REDACTED****"},
	{regexp.MustCompile(`(?i)-{5}BEGIN\s+(RSA\s+)?PRIVATE\s+KEY-{5}`), "****REDACTED PRIVATE KEY****"},
	{regexp.MustCompile(`(?i)-{5}BEGIN\s+CERTIFICATE-{5}`), "****REDACTED CERTIFICATE****"},
}

// SensitiveFieldNames lists field name substrings that indicate sensitive content.
var sensitiveFieldNames = []string{
	"password", "passwd", "pwd", "secret", "token",
	"api_key", "apikey", "private_key", "privkey",
	"cert_key", "tls_key", "credential",
}

// Sanitize redacts sensitive values from a string payload.
// Returns the sanitized string and a boolean indicating whether any
// content was redacted.
func Sanitize(payload string) (string, bool) {
	redacted := false
	result := payload

	for _, sp := range sensitivePatterns {
		if sp.pattern.MatchString(result) {
			result = sp.pattern.ReplaceAllString(result, sp.redactWith)
			redacted = true
		}
	}

	return result, redacted
}

// IsSensitiveField returns true if the field name indicates sensitive content.
func IsSensitiveField(fieldName string) bool {
	lower := strings.ToLower(fieldName)
	for _, name := range sensitiveFieldNames {
		if strings.Contains(lower, name) {
			return true
		}
	}
	return false
}

// RedactSensitive redacts a field value if the field name is sensitive.
// Returns the (possibly redacted) value.
func RedactSensitive(fieldName, value string) string {
	if IsSensitiveField(fieldName) {
		return "****REDACTED****"
	}
	return value
}
