package preflight

import "encoding/json"

// CommandPayload is the JSON body delivered in an outbox entry for a preflight stage.
// It carries enough context for the operator to execute the check and report results.
type CommandPayload struct {
	Stage       StageName `json:"stage"`
	OperationID string    `json:"operation_id"`
	BundleID    string    `json:"bundle_id,omitempty"`
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
