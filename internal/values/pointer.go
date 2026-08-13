package values

import (
	"fmt"
	"strconv"
	"strings"
)

// resolvePointer 按 RFC 6901 JSON Pointer（子集）解析 document 中的路径：
// 支持 ~0/~1 反转义与数组下标，返回目标值与其存在性。null 值视为存在
// （secret_refs 校验要求目标为 null 占位，见 ValidateWithRefs）。
func resolvePointer(document any, pointer string) (value any, exists bool, err error) {
	if pointer == "" {
		return document, true, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false, fmt.Errorf("pointer must start with slash")
	}
	current := document
	for _, rawToken := range strings.Split(pointer[1:], "/") {
		token, err := decodePointerToken(rawToken)
		if err != nil {
			return nil, false, err
		}
		switch typed := current.(type) {
		case map[string]any:
			current, exists = typed[token]
			if !exists {
				return nil, false, nil
			}
		case []any:
			if token == "-" || token == "" || (len(token) > 1 && token[0] == '0') {
				return nil, false, fmt.Errorf("invalid array index")
			}
			index, parseErr := strconv.Atoi(token)
			if parseErr != nil {
				return nil, false, fmt.Errorf("invalid array index: %w", parseErr)
			}
			if index < 0 || index >= len(typed) {
				return nil, false, nil
			}
			current = typed[index]
		default:
			return nil, false, nil
		}
	}
	return current, true, nil
}

// decodePointerToken 执行 RFC 6901 反转义：~0 → ~、~1 → /；非法转义报错。
func decodePointerToken(token string) (string, error) {
	var result strings.Builder
	result.Grow(len(token))
	for token != "" {
		plain, escaped, found := strings.Cut(token, "~")
		result.WriteString(plain)
		if !found {
			break
		}
		if escaped == "" {
			return "", fmt.Errorf("invalid pointer escape")
		}
		switch escaped[0] {
		case '0':
			result.WriteByte('~')
		case '1':
			result.WriteByte('/')
		default:
			return "", fmt.Errorf("invalid pointer escape")
		}
		token = escaped[1:]
	}
	return result.String(), nil
}
