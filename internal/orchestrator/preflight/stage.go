// Package preflight implements the release preflight orchestration stage (REQ-019).
package preflight

import "time"

// StageName identifies a discrete preflight validation stage.
type StageName string

const (
	StageArtifact    StageName = "artifact"
	StageRender      StageName = "render"
	StageCluster     StageName = "cluster"
	StageRuntimePull StageName = "runtime_pull"
)

// StageDef defines the policy for a single preflight stage.
type StageDef struct {
	Name     StageName
	Required bool
	Timeout  time.Duration
}

// ProductionStages returns the default production pipeline:
// Artifact → Render → Cluster (all required) + RuntimePull (optional).
func ProductionStages() []StageDef {
	return []StageDef{
		{Name: StageArtifact, Required: true, Timeout: 5 * time.Minute},
		{Name: StageRender, Required: true, Timeout: 5 * time.Minute},
		{Name: StageCluster, Required: true, Timeout: 10 * time.Minute},
		{Name: StageRuntimePull, Required: false, Timeout: 5 * time.Minute},
	}
}

// CommandType maps a stage name to its outbox command type string.
// Reuses REQ-016 command channel semantics with stage-specific prefix.
func CommandType(s StageName) string {
	switch s {
	case StageArtifact:
		return "PRECHECK_ARTIFACT"
	case StageRender:
		return "PRECHECK_RENDER"
	case StageCluster:
		return "PRECHECK_DRYRUN"
	case StageRuntimePull:
		return "PRECHECK_RUNTIME_PULL"
	default:
		return "PRECHECK_UNKNOWN"
	}
}
