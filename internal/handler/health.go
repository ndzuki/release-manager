package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Health returns an HTTP handler that responds with 200 OK.
func Health() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(map[string]string{
			"status": "ok",
		}); err != nil {
			slog.Error("health encode error", "error", err)
		}
	}
}
