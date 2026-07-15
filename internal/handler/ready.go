package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Ready returns an HTTP handler that reports readiness.
// It returns 200 if all dependencies are available, 503 otherwise.
func Ready(checks map[string]func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		results := make(map[string]string)
		healthy := true

		for name, check := range checks {
			if err := check(); err != nil {
				results[name] = err.Error()
				healthy = false
			} else {
				results[name] = "ok"
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if healthy {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}

		status := "ok"
		if !healthy {
			status = "degraded"
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"status": status,
			"checks": results,
		}); err != nil {
			slog.Error("ready encode error", "error", err)
		}
	}
}
