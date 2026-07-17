// Package legitimate_sdk uses the approved Helm Go SDK and must not trigger
// any SDK-only analyzer rule (AC-037-03).
package legitimate_sdk

import "helm.sh/helm/v3/pkg/action"

// NewInstallAction constructs a Helm SDK action without starting a subprocess.
func NewInstallAction() *action.Install {
	return action.NewInstall(&action.Configuration{})
}
