// Package legitimate_sdk uses only the Go standard library — no os/exec.
// This should NOT trigger any sdkcheck rule (AC-037-03).
package legitimate_sdk

import (
	"fmt"
	"net/http"
)

// HealthCheck uses net/http — legitimate, no os/exec involved.
func HealthCheck() error {
	resp, err := http.Get("http://localhost:8080/health")
	if err != nil {
		return fmt.Errorf("health check: %w", err)
	}
	defer resp.Body.Close()
	return nil
}
