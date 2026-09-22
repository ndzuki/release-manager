package operator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
)

// fakeArtifactLocator records how it was asked for the archive and returns the
// bytes the test configures, so the stage's digest parity is exercised without
// a registry.
type fakeArtifactLocator struct {
	archive []byte
	err     error
	calls   int
	ref     string
	version string
	plain   bool
}

func (f *fakeArtifactLocator) LocateChartArchive(ref, version string, plainHTTP bool) ([]byte, error) {
	f.calls++
	f.ref, f.version, f.plain = ref, version, plainHTTP
	if f.err != nil {
		return nil, f.err
	}
	return f.archive, nil
}

const artifactChartRef = "oci://registry.dev.release-manager.local:5000/release-fixture"

func artifactStageCommand(digest string) *operatorv1.Command {
	return &operatorv1.Command{
		CommandId: "cmd-artifact-1", OperationId: "op-1", Stage: "artifact",
		Bundle: &commonv1.ReleaseBundle{
			ChartRef: artifactChartRef, ChartVersion: "0.1.0", ChartDigest: digest,
		},
	}
}

// TASK-3 step 2 / ADR-024: the artifact stage compares the chart archive's
// content digest against the bundle's chart_digest and fails closed on every
// outcome that is not a verified match.
func TestArtifactStageDigestParity(t *testing.T) {
	archive := []byte("chart-archive-bytes")
	matching := chartArchiveDigest(archive)
	bare := strings.TrimPrefix(matching, "sha256:")

	tests := []struct {
		name         string
		command      *operatorv1.Command
		locator      *fakeArtifactLocator
		wantErr      string
		wantMismatch bool
		wantCalls    int
	}{
		{
			name:      "matching digest with the sha256 prefix passes",
			command:   artifactStageCommand(matching),
			locator:   &fakeArtifactLocator{archive: archive},
			wantCalls: 1,
		},
		{
			name:      "matching digest without the prefix and in uppercase passes",
			command:   artifactStageCommand(strings.ToUpper(bare)),
			locator:   &fakeArtifactLocator{archive: archive},
			wantCalls: 1,
		},
		{
			name:         "a digest that does not match the archive fails closed",
			command:      artifactStageCommand("sha256:" + strings.Repeat("0", 64)),
			locator:      &fakeArtifactLocator{archive: archive},
			wantErr:      "digest mismatch",
			wantMismatch: true,
			wantCalls:    1,
		},
		{
			name:      "a missing bundle fails closed before any fetch",
			command:   &operatorv1.Command{CommandId: "cmd-artifact-1", OperationId: "op-1", Stage: "artifact"},
			locator:   &fakeArtifactLocator{archive: archive},
			wantErr:   "requires a bundle",
			wantCalls: 0,
		},
		{
			name:      "an empty chart_digest fails closed before any fetch",
			command:   artifactStageCommand(""),
			locator:   &fakeArtifactLocator{archive: archive},
			wantErr:   "requires the bundle chart digest",
			wantCalls: 0,
		},
		{
			name:      "a whitespace-only chart_digest fails closed",
			command:   artifactStageCommand("   "),
			locator:   &fakeArtifactLocator{archive: archive},
			wantErr:   "requires the bundle chart digest",
			wantCalls: 0,
		},
		{
			name: "an empty chart reference fails closed before any fetch",
			command: func() *operatorv1.Command {
				cmd := artifactStageCommand(matching)
				cmd.Bundle.ChartRef = "  "
				return cmd
			}(),
			locator:   &fakeArtifactLocator{archive: archive},
			wantErr:   "requires a chart reference",
			wantCalls: 0,
		},
		{
			name:      "an unreachable archive fails closed",
			command:   artifactStageCommand(matching),
			locator:   &fakeArtifactLocator{err: assert.AnError},
			wantErr:   "artifact stage",
			wantCalls: 1,
		},
		{
			name:      "an empty archive fails closed",
			command:   artifactStageCommand(chartArchiveDigest(nil)),
			locator:   &fakeArtifactLocator{archive: []byte{}},
			wantErr:   "is empty",
			wantCalls: 1,
		},
		{
			name:      "a nil command fails closed",
			command:   nil,
			locator:   &fakeArtifactLocator{archive: archive},
			wantErr:   "requires a command",
			wantCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := NewArtifactStageExecutor(tt.locator, false, nil)

			result, err := executor.ExecuteStage(context.Background(), tt.command)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Empty(t, result, "a failed stage must not return a result a caller could read as a pass")
			} else {
				require.NoError(t, err)
				var decoded struct {
					Status string `json:"status"`
				}
				require.NoError(t, json.Unmarshal([]byte(result), &decoded))
				assert.Equal(t, "passed", decoded.Status)
			}
			if tt.wantMismatch {
				assert.ErrorIs(t, err, ErrArtifactDigestMismatch)
			}
			assert.Equal(t, tt.wantCalls, tt.locator.calls)
		})
	}
}

// A verified match reports the passed shape, carries the archive digest, and
// forwards the engine's plain-HTTP setting and the bundle's reference/version.
func TestArtifactStageReportsThePassedShape(t *testing.T) {
	archive := []byte("chart-archive-bytes")
	locator := &fakeArtifactLocator{archive: archive}
	executor := NewArtifactStageExecutor(locator, true, nil)

	result, err := executor.ExecuteStage(context.Background(), artifactStageCommand(chartArchiveDigest(archive)))
	require.NoError(t, err)

	assert.Equal(t, 1, locator.calls)
	assert.Equal(t, artifactChartRef, locator.ref)
	assert.Equal(t, "0.1.0", locator.version)
	assert.True(t, locator.plain, "the stage must pass the engine's plain-HTTP setting through")

	var decoded struct {
		Status      string `json:"status"`
		ChartDigest string `json:"chart_digest"`
		Detail      string `json:"detail"`
	}
	require.NoError(t, json.Unmarshal([]byte(result), &decoded))
	assert.Equal(t, "passed", decoded.Status)
	assert.Equal(t, chartArchiveDigest(archive), decoded.ChartDigest)
	assert.NotEmpty(t, decoded.Detail)
}

// A stage without a locator can never verify anything, so it must fail rather
// than report a pass.
func TestArtifactStageRequiresALocator(t *testing.T) {
	executor := NewArtifactStageExecutor(nil, false, nil)
	_, err := executor.ExecuteStage(context.Background(), artifactStageCommand("sha256:abc"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chart archive locator")

	var nilExecutor *ArtifactStageExecutor
	_, err = nilExecutor.ExecuteStage(context.Background(), artifactStageCommand("sha256:abc"))
	require.Error(t, err)
}

// A cancelled context stops the stage before it fetches the archive.
func TestArtifactStageStopsOnACancelledContext(t *testing.T) {
	locator := &fakeArtifactLocator{archive: []byte("chart-archive-bytes")}
	executor := NewArtifactStageExecutor(locator, false, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := executor.ExecuteStage(ctx, artifactStageCommand("sha256:abc"))
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, locator.calls)
}

// The digest comparison accepts the same spellings the install path accepts:
// the lowercase "sha256:" prefix is optional and the hex comparison is
// case-insensitive. A spelling the install path would reject must not pass here
// either, or the artifact stage would admit a digest the install then fails on.
func TestDigestValueMatches(t *testing.T) {
	digest := chartArchiveDigest([]byte("chart-archive-bytes"))
	bare := strings.TrimPrefix(digest, "sha256:")

	tests := []struct {
		name     string
		expected string
		actual   string
		want     bool
	}{
		{name: "identical prefixed values match", expected: digest, actual: digest, want: true},
		{name: "a bare expected value matches a prefixed actual value", expected: bare, actual: digest, want: true},
		{name: "an uppercase hex expected value matches", expected: "sha256:" + strings.ToUpper(bare), actual: digest, want: true},
		{name: "surrounding whitespace is ignored", expected: "  " + digest + "  ", actual: digest, want: true},
		{name: "a different digest does not match", expected: strings.Repeat("a", 64), actual: digest, want: false},
		{name: "a bare sha256 prefix does not match", expected: "sha256:", actual: digest, want: false},
		{name: "an empty expected value does not match", expected: "", actual: digest, want: false},
		{
			name:     "an uppercase prefix is not stripped, matching the install path",
			expected: "SHA256:" + bare, actual: digest, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, digestValueMatches(tt.expected, tt.actual))
		})
	}
}
