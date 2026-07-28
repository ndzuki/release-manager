// Package k8s resolves target-cluster Secret references without moving values across the control plane.
package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/ndzuki/release-manager/internal/operator/helmengine"
)

var pathToken = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\[\d+\])?$`)

// Resolve validates preflight snapshots and injects Secret values into a values document.
func Resolve(
	ctx context.Context,
	client corev1client.CoreV1Interface,
	namespace string,
	refs []*operatorv1.SecretRef,
	values map[string]any,
) (string, error) {
	if len(refs) == 0 {
		return digestSnapshot(nil), nil
	}
	if client == nil {
		return "", fmt.Errorf("%w: secret client is required", helmengine.ErrSecretRefChanged)
	}

	snapshots := make([]map[string]string, 0, len(refs))
	for _, ref := range refs {
		if ref == nil {
			return "", fmt.Errorf("%w: nil secret reference", helmengine.ErrSecretRefChanged)
		}
		secret, err := client.Secrets(namespace).Get(ctx, ref.GetName(), metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("%w: get: %w", helmengine.ErrSecretRefChanged, err)
		}
		value, ok := secret.Data[ref.GetKey()]
		if !ok {
			return "", fmt.Errorf("%w: key not found", helmengine.ErrSecretRefChanged)
		}
		valueDigest := sha256Hex(value)
		if ref.GetUid() != "" && ref.GetUid() != string(secret.UID) {
			return "", fmt.Errorf("%w: uid has changed", helmengine.ErrSecretRefChanged)
		}
		if ref.GetResourceVersion() != "" && ref.GetResourceVersion() != secret.ResourceVersion {
			return "", fmt.Errorf("%w: resource_version has changed", helmengine.ErrSecretRefChanged)
		}
		if ref.GetValueDigest() != "" && ref.GetValueDigest() != valueDigest {
			return "", fmt.Errorf("%w: value has changed", helmengine.ErrSecretRefChanged)
		}
		if err := setPath(values, ref.GetPath(), string(value)); err != nil {
			return "", fmt.Errorf("render_failed: %w", err)
		}
		snapshots = append(snapshots, map[string]string{
			"name":             ref.GetName(),
			"key":              ref.GetKey(),
			"uid":              string(secret.UID),
			"resource_version": secret.ResourceVersion,
			"value_digest":     valueDigest,
		})
	}
	return digestSnapshot(snapshots), nil
}

func setPath(document map[string]any, path, value string) error {
	if path == "" {
		return fmt.Errorf("secret path is required")
	}
	tokens := strings.Split(path, ".")
	var current any = document
	for index, token := range tokens {
		if !pathToken.MatchString(token) {
			return fmt.Errorf("invalid secret path %q", path)
		}
		name, arrayIndex, hasIndex, err := parseToken(token)
		if err != nil {
			return err
		}
		isLast := index == len(tokens)-1
		object, ok := current.(map[string]any)
		if !ok {
			return fmt.Errorf("secret path %q crosses a non-object", path)
		}
		next, exists := object[name]
		if !hasIndex {
			if isLast {
				object[name] = value
				return nil
			}
			if !exists {
				next = map[string]any{}
				object[name] = next
			}
			current = next
			continue
		}
		array, ok := next.([]any)
		if !ok || arrayIndex >= len(array) {
			return fmt.Errorf("secret path %q references an invalid array index", path)
		}
		if isLast {
			array[arrayIndex] = value
			return nil
		}
		current = array[arrayIndex]
	}
	return nil
}

func parseToken(token string) (name string, index int, hasIndex bool, err error) {
	open := strings.IndexByte(token, '[')
	if open < 0 {
		return token, 0, false, nil
	}
	index, err = strconv.Atoi(token[open+1 : len(token)-1])
	if err != nil {
		return "", 0, false, fmt.Errorf("invalid array index in %q", token)
	}
	return token[:open], index, true, nil
}

func digestSnapshot(snapshot any) string {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	return sha256Hex(encoded)
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return fmt.Sprintf("%x", sum)
}
