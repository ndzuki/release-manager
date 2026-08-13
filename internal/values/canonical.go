package values

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"

	yaml "gopkg.in/yaml.v3"
)

// parseDocument 解析单个 YAML/JSON 文档为通用值：剥离 BOM、拒绝 anchor/
// alias 与多文档（REQ-018 D11）。空输入视为空 map（canonical `{}`）。
func parseDocument(input []byte) (any, error) {
	input = stripBOM(input)
	if len(bytes.TrimSpace(input)) == 0 {
		return map[string]any{}, nil
	}

	decoder := yaml.NewDecoder(bytes.NewReader(input))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidYAML, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidYAML, err)
		}
		return nil, fmt.Errorf("%w: multiple documents are not allowed", ErrInvalidYAML)
	}
	if len(document.Content) == 0 {
		return map[string]any{}, nil
	}
	return nodeValue(document.Content[0])
}

// nodeValue 将 yaml.Node 解码为 Go 值：显式拒绝 anchor/alias（AliasNode/
// Anchor 非空），标量按 tag 区分 int/float/bool/null/string，防止隐式类型
// 混淆破坏 digest 稳定性。
//
//nolint:gocyclo // YAML node kinds and scalar tags require an explicit exhaustive decoder.
func nodeValue(node *yaml.Node) (any, error) {
	if node == nil {
		return nil, fmt.Errorf("%w: empty node", ErrInvalidYAML)
	}
	if node.Anchor != "" || node.Kind == yaml.AliasNode || node.Alias != nil {
		return nil, fmt.Errorf("%w: anchors and aliases are not allowed", ErrInvalidYAML)
	}
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) != 1 {
			return nil, fmt.Errorf("%w: invalid document", ErrInvalidYAML)
		}
		return nodeValue(node.Content[0])
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return nil, fmt.Errorf("%w: invalid mapping", ErrInvalidYAML)
		}
		result := make(map[string]any, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			keyNode := node.Content[index]
			if keyNode.Kind != yaml.ScalarNode || keyNode.Tag != "!!str" {
				return nil, fmt.Errorf("%w: mapping keys must be strings", ErrInvalidYAML)
			}
			if _, exists := result[keyNode.Value]; exists {
				return nil, fmt.Errorf("%w: duplicate mapping key %q", ErrInvalidYAML, keyNode.Value)
			}
			value, err := nodeValue(node.Content[index+1])
			if err != nil {
				return nil, err
			}
			result[keyNode.Value] = value
		}
		return result, nil
	case yaml.SequenceNode:
		result := make([]any, len(node.Content))
		for index, child := range node.Content {
			value, err := nodeValue(child)
			if err != nil {
				return nil, err
			}
			result[index] = value
		}
		return result, nil
	case yaml.ScalarNode:
		return scalarValue(node)
	default:
		return nil, fmt.Errorf("%w: unsupported YAML node", ErrInvalidYAML)
	}
}

// scalarValue 按 YAML tag 解码标量：null/str/bool/int 直接映射；
// float 包装为 canonicalFloat 保留 3.0 形态（不降级为 3）。
func scalarValue(node *yaml.Node) (any, error) {
	switch node.Tag {
	case "!!null":
		return nil, nil
	case "!!str":
		return node.Value, nil
	case "!!bool":
		value, err := strconv.ParseBool(node.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid boolean", ErrInvalidYAML)
		}
		return value, nil
	case "!!int":
		value, err := strconv.ParseInt(node.Value, 0, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: integer out of range", ErrInvalidYAML)
		}
		return value, nil
	case "!!float":
		value, err := strconv.ParseFloat(node.Value, 64)
		if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return nil, fmt.Errorf("%w: invalid float", ErrInvalidYAML)
		}
		return canonicalFloat(value), nil
	default:
		return nil, fmt.Errorf("%w: unsupported scalar tag %s", ErrInvalidYAML, node.Tag)
	}
}

type canonicalFloat float64

func (value canonicalFloat) MarshalJSON() ([]byte, error) {
	encoded := strconv.FormatFloat(float64(value), 'g', -1, 64)
	if !bytes.ContainsAny([]byte(encoded), ".eE") {
		encoded += ".0"
	}
	return []byte(encoded), nil
}

// marshalCanonical 递归编码 canonical JSON：map key 字节序排序、数组保序、
// null 保留、int/float 不混淆（整值 float 输出 3.0 形态，canonicalFloat
// 覆写 MarshalJSON 保证）。
func marshalCanonical(document any) ([]byte, error) {
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("canonical marshal: %w", err)
	}
	return encoded, nil
}
