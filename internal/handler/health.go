package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Health returns an HTTP handler that responds with 200 OK. Optional providers
// contribute JSON sub-objects to the response body (e.g. the gc status block
// of the release-orchestrator /health endpoint).
func Health(providers ...func() any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		body := map[string]any{"status": "ok"}
		for _, provider := range providers {
			if provider == nil {
				continue
			}
			if sub := provider(); sub != nil {
				body["gc"] = sub
			}
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			slog.Error("health encode error", "error", err)
		}
	}
}
