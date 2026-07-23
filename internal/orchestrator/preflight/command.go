package preflight

import (
	"encoding/json"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
)

// CommandPayload is the JSON body delivered in an outbox entry for a preflight stage.
// It carries enough context for the operator to execute the check and report results.
type CommandPayload struct {
	Stage                   StageName               `json:"stage"`
	OperationID             string                  `json:"operation_id"`
	BundleID                string                  `json:"bundle_id,omitempty"`
	DefinitionID            string                  `json:"definition_id,omitempty"`
	Bundle                  *commonv1.ReleaseBundle `json:"bundle,omitempty"`
	Namespace               string                  `json:"namespace,omitempty"`
	ReleaseName             string                  `json:"release_name,omitempty"`
	Values                  json.RawMessage         `json:"values,omitempty"`
	ValuesRevisionID        string                  `json:"values_revision_id,omitempty"`
	ValuesPatch             json.RawMessage         `json:"values_patch,omitempty"`
	ExpectedCurrentRevision int                     `json:"expected_current_revision,omitempty"`
	TargetRevision          int                     `json:"target_revision,omitempty"`
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
