package main

import (
	"fmt"

	"github.com/ndzuki/release-manager/internal/config"
)

// legacyOperatorEndpoint is the pre-advertise fallback kept byte-for-byte for
// dev and e2e: the in-cluster operator Service when the gateway listener is
// disabled, and the dev gateway host name when it is enabled. It lives in this
// file rather than main.go so calling it from operatorEndpoint does not shift
// the line numbers of cmd/orchestrator/main.go (docs/citations.assert pins
// them).
func legacyOperatorEndpoint(gatewayCfg config.GatewayCfg) string {
	if gatewayCfg.Enabled {
		return fmt.Sprintf("https://operator-gateway.dev.release-manager.local:%d", gatewayCfg.Port)
	}
	return "http://operator:8084"
}
