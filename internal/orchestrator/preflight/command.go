package preflight

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/store"
	"github.com/ndzuki/release-manager/internal/values"
	"google.golang.org/protobuf/types/known/durationpb"
)

// CommandPayload is the JSON body delivered in an outbox entry for a preflight stage.
// It carries enough context for the operator to execute the check and report results.
type CommandPayload struct {
	Stage                   StageName                  `json:"stage"`
	OperationID             string                     `json:"operation_id"`
	BundleID                string                     `json:"bundle_id,omitempty"`
	PayloadVersion          uint32                     `json:"payload_version,omitempty"`
	Upgrade                 *operatorv1.UpgradeCommand `json:"upgrade,omitempty"`
	DefinitionID            string                     `json:"definition_id,omitempty"`
	Namespace               string                     `json:"namespace,omitempty"`
	ReleaseName             string                     `json:"release_name,omitempty"`
	TimeoutSeconds          int64                      `json:"timeout_seconds,omitempty"`
	Bundle                  *commonv1.ReleaseBundle    `json:"bundle,omitempty"`
	Values                  json.RawMessage            `json:"values,omitempty"`
	ValuesRevisionID        string                     `json:"values_revision_id,omitempty"`
	ExpectedCurrentRevision int64                      `json:"expected_current_revision,omitempty"`
	TargetRevision          int64                      `json:"target_revision,omitempty"`
	Atomic                  bool                       `json:"atomic,omitempty"`
	ValuesPatch             json.RawMessage            `json:"values_patch,omitempty"`
}

// Marshal serializes the payload to JSON bytes.
func (p *CommandPayload) Marshal() ([]byte, error) {
	return json.Marshal(p)
}

// UnmarshalCommandPayload deserializes a command payload from raw bytes.
func UnmarshalCommandPayload(data []byte) (*CommandPayload, error) {
	var p CommandPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// BuildUpgradeCommand freezes all non-sensitive execution inputs for one upgrade.
func BuildUpgradeCommand(
	op *store.Operation,
	definition *store.ReleaseDefinition,
	bundle *store.ReleaseBundle,
	revision *store.ValuesRevision,
	commandID string,
) (*operatorv1.UpgradeCommand, error) {
	if op == nil || definition == nil || bundle == nil || revision == nil {
		return nil, fmt.Errorf("upgrade command inputs are required")
	}

	effectiveValues, err := mergeEffectiveValues(revision.Values, op.ValuesPatch, bundle.Images)
	if err != nil {
		return nil, err
	}
	secretRefs, err := decodeSecretRefs(revision.SecretRefs)
	if err != nil {
		return nil, fmt.Errorf("decode secret refs: %w", err)
	}

	timeout := durationpb.New(5 * time.Minute)
	if op.Deadline != nil {
		duration := time.Until(*op.Deadline)
		if duration > 0 {
			timeout = durationpb.New(duration)
		}
	}

	return &operatorv1.UpgradeCommand{
		DefinitionId: definition.ID,
		Namespace:    definition.Namespace,
		ReleaseName:  definition.ReleaseName,
		Bundle: &operatorv1.ReleaseBundleSnapshot{
			BundleId: bundle.ID, BundleDigest: bundle.DigestAlg + ":" + bundle.DigestValue,
			ChartRef: bundle.ChartRef, ChartVersion: bundle.ChartVersion, ChartDigest: bundle.ChartDigest,
		},
		Chart: &operatorv1.ChartReference{
			ResolvedUri: bundle.ChartRef, Version: bundle.ChartVersion, Digest: bundle.ChartDigest,
		},
		EffectiveValuesJson: effectiveValues, EffectiveValuesDigest: values.Digest(effectiveValues),
		SecretRefs:  secretRefs,
		OperationId: op.ID, CommandId: commandID,
		ExpectedRevision: uint64(op.ExpectedRevision), //nolint:gosec // UPGRADE validation requires a positive SDK revision.
		Atomic:           true, Timeout: timeout, MaxHistory: 10,
	}, nil
}

// BuildUpgradePayload constructs the versioned outbox envelope for an Upgrade operation.
func BuildUpgradePayload(
	op *store.Operation,
	definition *store.ReleaseDefinition,
	bundle *store.ReleaseBundle,
	revision *store.ValuesRevision,
	commandID string,
) (*CommandPayload, error) {
	upgrade, err := BuildUpgradeCommand(op, definition, bundle, revision, commandID)
	if err != nil {
		return nil, err
	}
	return &CommandPayload{OperationID: op.ID, BundleID: op.BundleID, PayloadVersion: 2, Upgrade: upgrade}, nil
}

func mergeEffectiveValues(base, patch []byte, images []store.BundleImage) ([]byte, error) {
	baseCanonical, err := values.Canonicalize(base)
	if err != nil {
		return nil, fmt.Errorf("canonicalize approved values: %w", err)
	}
	var document map[string]any
	if err := json.Unmarshal(baseCanonical, &document); err != nil {
		return nil, fmt.Errorf("decode approved values: %w", err)
	}
	if len(patch) > 0 {
		var patchDocument map[string]any
		if err := json.Unmarshal(patch, &patchDocument); err != nil {
			return nil, fmt.Errorf("decode values patch: %w", err)
		}
		mergeJSON(document, patchDocument)
	}
	for _, image := range images {
		if err := setValuesPath(document, image.ValuesPath, normalizeImageReference(image)); err != nil {
			return nil, fmt.Errorf("render_failed: %w", err)
		}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("marshal effective values: %w", err)
	}
	return encoded, nil
}

func mergeJSON(target, patch map[string]any) {
	for key, value := range patch {
		if value == nil {
			delete(target, key)
			continue
		}
		patchMap, ok := value.(map[string]any)
		if !ok {
			target[key] = value
			continue
		}
		targetMap, ok := target[key].(map[string]any)
		if !ok {
			targetMap = map[string]any{}
			target[key] = targetMap
		}
		mergeJSON(targetMap, patchMap)
	}
}

func setValuesPath(document map[string]any, path, value string) error {
	if path == "" {
		return fmt.Errorf("image values_path is required")
	}
	segments := strings.Split(path, ".")
	current := document
	for _, segment := range segments[:len(segments)-1] {
		next, ok := current[segment].(map[string]any)
		if !ok {
			return fmt.Errorf("values_path %q does not reference an object", path)
		}
		current = next
	}
	leaf := segments[len(segments)-1]
	if existing, ok := current[leaf]; ok {
		if _, ok := existing.(string); !ok {
			return fmt.Errorf("values_path %q does not accept a string", path)
		}
	}
	current[leaf] = value
	return nil
}

func normalizeImageReference(image store.BundleImage) string {
	if strings.HasPrefix(image.Digest, "sha256:") {
		return image.Ref + "@" + image.Digest
	}
	return image.Ref + "@sha256:" + image.Digest
}

func decodeSecretRefs(raw []byte) ([]*operatorv1.SecretRef, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var refs []*operatorv1.SecretRef
	if err := json.Unmarshal(raw, &refs); err != nil {
		return nil, err
	}
	return refs, nil
}
