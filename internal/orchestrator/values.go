package orchestrator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	jsonpatch "gopkg.in/evanphx/json-patch.v4"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ndzuki/release-manager/internal/store"
	valueutil "github.com/ndzuki/release-manager/internal/values"
)

var (
	errInvalidMergePatch      = errors.New("invalid_merge_patch")
	errSecretLiteralForbidden = errors.New("secret_literal_forbidden")
)

// mergedValues contains the canonical values artifacts persisted with an operation.
type mergedValues struct {
	patch           []byte
	patchDigest     string
	effective       []byte
	effectiveDigest string
}

// prepareValues canonicalizes the JSON Merge Patch (AC-067-14), merges it onto
// the approved revision, and runs the secret-literal scan on base, patch, and
// effective values (AC-067-12). A nil patch is equivalent to {}.
func prepareValues(revision *store.ValuesRevision, patch *structpb.Struct) (*mergedValues, error) {
	base, err := valueutil.Canonicalize(revision.Values)
	if err != nil {
		return nil, fmt.Errorf("%w: values revision is not valid JSON", errInvalidMergePatch)
	}
	if valueutil.ContainsSecret(base) {
		return nil, errSecretLiteralForbidden
	}
	if !isJSONObject(base) {
		return nil, fmt.Errorf("%w: values revision must be an object", errInvalidMergePatch)
	}

	patchBytes := []byte(`{}`)
	if patch != nil {
		raw, err := json.Marshal(patch.AsMap())
		if err != nil {
			return nil, fmt.Errorf("%w: patch is not valid JSON Merge Patch", errInvalidMergePatch)
		}
		patchBytes, err = valueutil.Canonicalize(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: patch is not valid JSON Merge Patch", errInvalidMergePatch)
		}
	}
	if !isJSONObject(patchBytes) {
		return nil, fmt.Errorf("%w: patch must be an object", errInvalidMergePatch)
	}
	if valueutil.ContainsSecret(patchBytes) {
		return nil, errSecretLiteralForbidden
	}

	effective, err := jsonpatch.MergePatch(base, patchBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: merge failed", errInvalidMergePatch)
	}
	effective, err = valueutil.Canonicalize(effective)
	if err != nil || !isJSONObject(effective) {
		return nil, fmt.Errorf("%w: merged values must be an object", errInvalidMergePatch)
	}
	if valueutil.ContainsSecret(effective) {
		return nil, errSecretLiteralForbidden
	}

	return &mergedValues{
		patch:           patchBytes,
		patchDigest:     valueutil.Digest(patchBytes),
		effective:       effective,
		effectiveDigest: valueutil.Digest(effective),
	}, nil
}

func isJSONObject(document []byte) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(bytes.TrimSpace(document), &object) == nil && object != nil
}
