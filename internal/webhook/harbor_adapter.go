package webhook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"connectrpc.com/connect"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
)

// CloudEvent represents a minimal CloudEvents 1.0 envelope.
type CloudEvent struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Time string          `json:"time"`
	Data json.RawMessage `json:"data"`
}

// HarborPushData carries the relevant subset of a Harbor artifact push event.
type HarborPushData struct {
	Resources  []HarborResource `json:"resources"`
	Repository struct {
		Name string `json:"name"`
	} `json:"repository"`
}

// HarborResource represents one artifact resource in a Harbor event.
type HarborResource struct {
	Digest      string `json:"digest"`
	Tag         string `json:"tag"`
	ResourceURL string `json:"resource_url"`
}

// NewHarborHandler returns an HTTP handler that parses CloudEvents 1.0
// from Harbor and forwards them to orchestrator as RecordArtifactEvent.
func NewHarborHandler(client orchestratorv1connect.BundleServiceClient, sourceID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var event CloudEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			http.Error(w, "invalid cloud event", http.StatusBadRequest)
			return
		}
		if event.Type != "harbor.artifact.pushed" {
			http.Error(w, "unsupported event type: "+event.Type, http.StatusBadRequest)
			return
		}
		if event.ID == "" {
			http.Error(w, "cloud event id is required", http.StatusBadRequest)
			return
		}

		var data HarborPushData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			http.Error(w, "invalid harbor push data", http.StatusBadRequest)
			return
		}
		if len(data.Resources) == 0 || len(data.Resources) > 100 {
			http.Error(w, "resources count must be between 1 and 100", http.StatusBadRequest)
			return
		}

		rawPayload, _ := json.Marshal(event)
		payloadHash := sha256.Sum256(rawPayload)

		resources := make([]*commonv1.EventResource, len(data.Resources))
		for index, res := range data.Resources {
			resources[index] = &commonv1.EventResource{
				Digest: res.Digest,
				Ref:    res.ResourceURL,
				Tag:    res.Tag,
			}
		}

		req := connect.NewRequest(&orchestratorv1.RecordArtifactEventRequest{
			SourceId:      sourceID,
			EventId:       event.ID,
			EventType:     event.Type,
			OccurredAt:    event.Time,
			RawPayload:    string(rawPayload),
			PayloadSha256: hex.EncodeToString(payloadHash[:]),
			ArtifactType:  commonv1.ArtifactType_ARTIFACT_TYPE_IMAGE,
			Repository:    data.Repository.Name,
			Resources:     resources,
		})

		resp, err := client.RecordArtifactEvent(r.Context(), req)
		if err != nil {
			code := connect.CodeOf(err)
			if code == connect.CodeAlreadyExists {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]string{"status": "duplicate"})
				return
			}
			http.Error(w, "orchestrator error", http.StatusServiceUnavailable)
			return
		}

		_ = resp
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "recorded"})
	})
}
