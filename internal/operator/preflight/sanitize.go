package preflight

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// sanitizeResource removes Secret data and stringData from an unstructured
// object before logging or persisting. It creates a shallow copy to avoid
// mutating the original.
func sanitizeResource(obj *unstructured.Unstructured) *unstructured.Unstructured {
	if obj == nil {
		return nil
	}

	gvk := obj.GroupVersionKind()
	if gvk.Kind != "Secret" || gvk.Group != "" {
		return obj
	}

	cp := obj.DeepCopy()
	content := cp.UnstructuredContent()

	delete(content, "data")
	delete(content, "stringData")

	return cp
}

// sanitizeResultJSON ensures the result JSON used for logging/storage never
// contains raw Secret values. Any field named "data" or "stringData" at the
// top level of a Secret is removed.
func sanitizeResultJSON(jsonStr, gvkShort string) string {
	if jsonStr == "" {
		return ""
	}
	if !strings.Contains(gvkShort, "Secret") && !strings.Contains(gvkShort, "secret") {
		return jsonStr
	}
	cleaned := jsonStr
	for _, key := range []string{`"data":`, `"stringData":`} {
		cleaned = stripJSONField(cleaned, key)
	}
	return cleaned
}

// stripJSONField removes a top-level JSON field from a compact JSON object.
// It is a best-effort post-processing pass; the primary defense is at the
// unstructured serialization boundary.
func stripJSONField(s, key string) string {
	for {
		start := strings.Index(s, key)
		if start < 0 {
			return s
		}
		end := findJSONFieldEnd(s, start+len(key))
		if end < 0 {
			return s
		}
		prefix := strings.TrimRight(s[:start], ", \t\n")
		suffix := s[end:]
		// Drop trailing comma.
		if strings.HasPrefix(strings.TrimSpace(suffix), ",") {
			commaIdx := strings.Index(suffix, ",")
			suffix = suffix[commaIdx+1:]
		}
		s = prefix + suffix
	}
}

// findJSONFieldEnd finds the closing brace/bracket/quote after a field value.
func findJSONFieldEnd(s string, pos int) int {
	if pos >= len(s) {
		return -1
	}
	depth := 0
	inString := false
	for i := pos; i < len(s); i++ {
		ch := s[i]
		if inString {
			if ch == '\\' {
				i++ // skip escaped char
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth < 0 {
				return i + 1
			}
		case ',':
			if depth == 0 {
				return i
			}
		}
	}
	if depth <= 0 {
		return len(s)
	}
	return -1
}

// sanitizeError strips sensitive data from error messages meant for output.
