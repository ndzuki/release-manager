package helmengine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

func mergeRenderValues(base, patch []byte, overrides []ImageOverride) (map[string]interface{}, error) {
	values, err := decodeJSONObject(base, "values")
	if err != nil {
		return nil, err
	}

	if len(patch) > 0 {
		patchValues, err := decodeJSONObject(patch, "values patch")
		if err != nil {
			return nil, err
		}
		values = mergeJSONObject(values, patchValues)
	}

	for _, override := range overrides {
		if err := setValuesPath(values, override.Path, override.Image); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func decodeJSONObject(data []byte, label string) (map[string]interface{}, error) {
	if len(data) == 0 {
		return map[string]interface{}{}, nil
	}

	var value interface{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode %s: %w", label, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode %s: %w", label, err)
	}

	object, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("decode %s: expected JSON object", label)
	}
	return object, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing interface{}
	if err := decoder.Decode(&trailing); err == nil {
		return errors.New("unexpected trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func mergeJSONObject(base, patch map[string]interface{}) map[string]interface{} {
	for key, patchValue := range patch {
		if patchValue == nil {
			delete(base, key)
			continue
		}

		patchObject, patchIsObject := patchValue.(map[string]interface{})
		baseObject, baseIsObject := base[key].(map[string]interface{})
		if patchIsObject {
			if !baseIsObject {
				baseObject = make(map[string]interface{}, len(patchObject))
			}
			base[key] = mergeJSONObject(baseObject, patchObject)
			continue
		}
		base[key] = patchValue
	}
	return base
}

func setValuesPath(values map[string]interface{}, path, image string) error {
	parts := strings.Split(path, ".")
	if path == "" || image == "" {
		return fmt.Errorf("image override path and image are required")
	}
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("image override path %q is invalid", path)
		}
	}

	current := values
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part]
		if !ok {
			child := map[string]interface{}{}
			current[part] = child
			current = child
			continue
		}
		child, ok := next.(map[string]interface{})
		if !ok {
			return fmt.Errorf("image override path %q crosses non-object value", path)
		}
		current = child
	}
	current[parts[len(parts)-1]] = image
	return nil
}
