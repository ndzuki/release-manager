package values

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

var (
	defaultSecretKeyPattern = regexp.MustCompile(`(?i)(?:password|passwd|secret|token|api[_-]?key|apikey|private[_-]?key|access[_-]?key|credential|client[_-]?secret)`)
	awsAccessKeyPattern     = regexp.MustCompile(`\bAKIA[A-Z0-9]{16}\b`)
	privateKeyPattern       = regexp.MustCompile(`-{5}BEGIN [A-Z0-9 ]*PRIVATE KEY-{5}`)
	credentialURIPattern    = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s/:@]+:[^\s/@]+@`)
	hexValuePattern         = regexp.MustCompile(`(?i)^[0-9a-f]{32,}$`)
	base64ValuePattern      = regexp.MustCompile(`^[A-Za-z0-9+/]{32,}={0,2}$`)
)

func containsSecret(document any, extraPatterns []string) bool {
	patterns := make([]*regexp.Regexp, 0, len(extraPatterns)+1)
	patterns = append(patterns, defaultSecretKeyPattern)
	for _, raw := range extraPatterns {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		pattern, err := regexp.Compile(`(?i)(?:` + raw + `)`)
		if err != nil {
			return true
		}
		patterns = append(patterns, pattern)
	}
	return inspectSecret(document, "", patterns)
}

func inspectSecret(value any, key string, keyPatterns []*regexp.Regexp) bool {
	if key != "" && secretKey(key, keyPatterns) && !secretPlaceholder(value) {
		return true
	}
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			if inspectSecret(child, childKey, keyPatterns) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if inspectSecret(child, "", keyPatterns) {
				return true
			}
		}
	case string:
		return secretValueShape(typed)
	}
	return false
}

func secretKey(key string, patterns []*regexp.Regexp) bool {
	for _, pattern := range patterns {
		if pattern.MatchString(key) {
			return true
		}
	}
	return false
}

func secretPlaceholder(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	text = strings.TrimSpace(text)
	return text == "" || strings.HasPrefix(text, "${") || strings.HasPrefix(text, "{{") ||
		strings.HasPrefix(strings.ToLower(text), "ref ") || text == "<ref>" || text == "[ref]"
}

func secretValueShape(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	if awsAccessKeyPattern.MatchString(trimmed) || privateKeyPattern.MatchString(trimmed) || credentialURIPattern.MatchString(trimmed) {
		return true
	}
	if hexValuePattern.MatchString(trimmed) || base64ValuePattern.MatchString(trimmed) {
		return highEntropy(trimmed)
	}
	return false
}

func highEntropy(value string) bool {
	counts := make(map[rune]float64)
	var total float64
	for _, char := range value {
		if unicode.IsSpace(char) {
			continue
		}
		counts[char]++
		total++
	}
	if total < 32 {
		return false
	}
	var entropy float64
	for _, count := range counts {
		probability := count / total
		entropy -= probability * math.Log2(probability)
	}
	return entropy >= 3.5
}
