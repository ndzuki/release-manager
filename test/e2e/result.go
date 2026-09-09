package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StageStatus is the outcome of one selected stage.
type StageStatus string

const (
	StagePass StageStatus = "pass"
	StageFail StageStatus = "fail"
	StageSkip StageStatus = "skip"
)

// ErrorCause is a sanitized, machine-readable cause attached to a stage.
type ErrorCause struct {
	Code      string `json:"code"`
	Component string `json:"component,omitempty"`
	Message   string `json:"message,omitempty"`
}

// ArtifactRef references a safe diagnostic artifact without embedding payloads.
type ArtifactRef struct {
	ID     string `json:"id"`
	Digest string `json:"digest,omitempty"`
}

// OperationRef references a public operation observation.
type OperationRef struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	FinalState string `json:"final_state,omitempty"`
}

// StageResult is the stable JSON representation of one selected stage.
type StageResult struct {
	Stage      string         `json:"stage"`
	Status     StageStatus    `json:"status"`
	StartedAt  time.Time      `json:"started_at"`
	DurationMs int64          `json:"duration_ms"`
	RootCause  string         `json:"root_cause,omitempty"`
	ErrorCode  string         `json:"error_code,omitempty"`
	Causes     []ErrorCause   `json:"causes,omitempty"`
	Artifacts  []ArtifactRef  `json:"artifacts,omitempty"`
	Operations []OperationRef `json:"operations,omitempty"`

	// Legacy fields remain as an in-process compatibility seam for existing
	// deterministic Stage tests; they are never serialized into artifacts.
	Name     string        `json:"-"`
	Error    error         `json:"-"`
	Duration time.Duration `json:"-"`
	Cause    string        `json:"-"`
}

// RunArtifact is the stable run.json representation consumed by CI.
type RunArtifact struct {
	RunID          string          `json:"run_id"`
	SelectedStages []string        `json:"selected_stages"`
	BaselineDigest string          `json:"baseline_digest,omitempty"`
	StartedAt      time.Time       `json:"started_at"`
	FinishedAt     time.Time       `json:"finished_at,omitempty"`
	Pass           int             `json:"pass"`
	Fail           int             `json:"fail"`
	Skip           int             `json:"skip"`
	ExitCode       int             `json:"exit_code"`
	Fatal          *ErrorCause     `json:"fatal,omitempty"`
	Stages         []StageArtifact `json:"stages"`
}

// StageArtifact links one selected stage to its persisted result.
type StageArtifact struct {
	Stage  string      `json:"stage"`
	Status StageStatus `json:"status"`
	File   string      `json:"file"`
}

func (r StageResult) String() string {
	return fmt.Sprintf("%s: %s (%dms)", r.Stage, r.Status, r.DurationMs)
}

// WriteJSONAtomic writes a JSON artifact through a same-directory temporary
// file and rename, preventing readers from observing partial output.
func WriteJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create artifact directory: %w", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal artifact: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".artifact-*")
	if err != nil {
		return fmt.Errorf("create artifact temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod artifact temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write artifact temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync artifact temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close artifact temporary file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename artifact temporary file: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(path)); err != nil {
		return fmt.Errorf("open artifact directory for sync: %w", err)
	} else {
		defer func() { _ = dir.Close() }()
		if err := dir.Sync(); err != nil {
			return fmt.Errorf("sync artifact directory: %w", err)
		}
	}
	return nil
}

// PrepareOutputDir removes only this runner's fixed artifacts.
func PrepareOutputDir(dir string, stages []string) error {
	fixed := append([]string{"run.json", "baseline.json"}, stageArtifactNames(stages)...)
	for _, name := range fixed {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale artifact %s: %w", name, err)
		}
	}
	return nil
}

func stageArtifactNames(stages []string) []string {
	copyStages := append([]string(nil), stages...)
	sort.Strings(copyStages)
	out := make([]string, 0, len(copyStages))
	for _, stage := range copyStages {
		if strings.TrimSpace(stage) != "" {
			out = append(out, stage+".json")
		}
	}
	return out
}
