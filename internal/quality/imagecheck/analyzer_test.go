package imagecheck

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnalyze_CompliantImage(t *testing.T) {
	policy := testPolicy()
	dockerfile := writeDockerfile(t, policy.BaseImage)
	archive := buildDockerArchive(t, imageConfigInput{
		Entrypoint: []string{"/release-operator"},
		Cmd:        []string{"--config", "/configs/operator.dev.yaml"},
	}, []layerEntry{
		{Name: "release-operator", Mode: 0o755},
		{Name: "configs/operator.dev.yaml", Mode: 0o644},
	})

	result, err := Analyze(bytes.NewReader(archive), dockerfile, policy)
	require.NoError(t, err)
	assert.True(t, result.Passed())
	assert.Empty(t, result.Violations)
}

func TestAnalyze_WhiteoutRemovesForbiddenBinary(t *testing.T) {
	policy := testPolicy()
	dockerfile := writeDockerfile(t, policy.BaseImage)
	archive := buildDockerArchive(t, imageConfigInput{
		Entrypoint: []string{"/release-operator"},
	}, []layerEntry{
		{Name: "release-operator", Mode: 0o755},
		{Name: "usr/bin/helm", Mode: 0o755},
	}, []layerEntry{
		{Name: "usr/bin/.wh.helm", Mode: 0o000},
	})

	result, err := Analyze(bytes.NewReader(archive), dockerfile, policy)
	require.NoError(t, err)
	assert.True(t, result.Passed())
}

func TestAnalyze_ReportsCompositionViolations(t *testing.T) {
	policy := testPolicy()
	dockerfile := writeDockerfile(t, "gcr.io/distroless/static-debian13:nonroot")
	archive := buildDockerArchive(t, imageConfigInput{
		Entrypoint: []string{"/bin/helm"},
		Cmd:        []string{"kubectl"},
	}, []layerEntry{
		{Name: "release-operator", Mode: 0o644},
		{Name: "usr/local/bin/sidecar", Mode: 0o755},
		{Name: "bin/helm", Mode: 0o755},
	})

	result, err := Analyze(bytes.NewReader(archive), dockerfile, policy)
	require.NoError(t, err)
	assert.False(t, result.Passed())
	assertRule(t, result, RuleBaseImageUnpinned)
	assertRule(t, result, RuleEntrypointMismatch)
	assertRule(t, result, RuleBinaryDependencyDetected)
	assertRule(t, result, RuleUnexpectedExecutable)
	assertRule(t, result, RulePathCheckFailed)
}

func TestLoadPolicy_RejectsForbiddenAllowlistEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`version: "1"
gate: operator-image-sdk-only
base_image: "gcr.io/distroless/static-debian13:nonroot@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
required_entrypoint: ["/release-operator"]
allowed_executables: ["/usr/bin/kubectl"]
forbidden_basenames: ["helm", "kubectl"]
`), 0o644))

	_, err := LoadPolicy(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), RuleBinaryDependencyDetected)
}

func assertRule(t *testing.T, result Result, ruleID string) {
	t.Helper()
	for _, violation := range result.Violations {
		if violation.RuleID == ruleID {
			return
		}
	}
	t.Fatalf("result missing rule %q: %+v", ruleID, result.Violations)
}

func testPolicy() Policy {
	return Policy{
		Version:            "1",
		Gate:               "operator-image-sdk-only",
		BaseImage:          "gcr.io/distroless/static-debian13:nonroot@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RequiredEntrypoint: []string{"/release-operator"},
		AllowedExecutables: []string{"/release-operator"},
		ForbiddenBasenames: []string{"helm", "kubectl"},
		AdditionalPathChecks: []PathCheck{
			{Path: "/release-operator", MustExist: true, ExpectedType: "executable"},
			{Path: "/bin/sh", MustNotExist: true},
		},
	}
}

func writeDockerfile(t *testing.T, baseImage string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Dockerfile.operator")
	contents := "FROM golang:1.26 AS builder\nFROM " + baseImage + "\n"
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	return path
}

type imageConfigInput struct {
	Entrypoint []string
	Cmd        []string
}

type layerEntry struct {
	Name     string
	Mode     int64
	Type     byte
	Linkname string
}

func buildDockerArchive(t *testing.T, configInput imageConfigInput, layers ...[]layerEntry) []byte {
	t.Helper()

	config := map[string]any{
		"config": map[string]any{
			"Entrypoint": configInput.Entrypoint,
			"Cmd":        configInput.Cmd,
		},
	}
	configData, err := json.Marshal(config)
	require.NoError(t, err)

	layerNames := make([]string, len(layers))
	layerData := make([][]byte, len(layers))
	for index, entries := range layers {
		layerNames[index] = filepath.ToSlash(filepath.Join("layer", string(rune('a'+index)), "layer.tar"))
		layerData[index] = buildLayer(t, entries)
	}
	manifestData, err := json.Marshal([]dockerManifestEntry{{
		Config: "config.json",
		Layers: layerNames,
	}})
	require.NoError(t, err)

	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	writeTarFile(t, writer, "manifest.json", manifestData, 0o644, tar.TypeReg, "")
	writeTarFile(t, writer, "config.json", configData, 0o644, tar.TypeReg, "")
	for index, name := range layerNames {
		writeTarFile(t, writer, name, layerData[index], 0o644, tar.TypeReg, "")
	}
	require.NoError(t, writer.Close())
	return archive.Bytes()
}

func buildLayer(t *testing.T, entries []layerEntry) []byte {
	t.Helper()

	var layer bytes.Buffer
	writer := tar.NewWriter(&layer)
	for _, entry := range entries {
		entryType := entry.Type
		if entryType == 0 {
			entryType = tar.TypeReg
		}
		writeTarFile(t, writer, entry.Name, []byte("fixture"), entry.Mode, entryType, entry.Linkname)
	}
	require.NoError(t, writer.Close())
	return layer.Bytes()
}

func writeTarFile(
	t *testing.T,
	writer *tar.Writer,
	name string,
	data []byte,
	mode int64,
	typeFlag byte,
	linkname string,
) {
	t.Helper()
	if typeFlag == tar.TypeSymlink || typeFlag == tar.TypeLink {
		data = nil
	}
	require.NoError(t, writer.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     mode,
		Size:     int64(len(data)),
		Typeflag: typeFlag,
		Linkname: linkname,
	}))
	if len(data) > 0 {
		_, err := writer.Write(data)
		require.NoError(t, err)
	}
}
