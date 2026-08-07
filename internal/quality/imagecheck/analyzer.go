// Package imagecheck validates a Docker image archive against an executable policy.
package imagecheck

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Stable rule identifiers emitted by the image gate.
const (
	RuleUnexpectedExecutable     = "unexpected_executable"
	RuleBinaryDependencyDetected = "binary_dependency_detected"
	RuleBaseImageUnpinned        = "base_image_unpinned"
	RuleEntrypointMismatch       = "entrypoint_mismatch"
	RulePathCheckFailed          = "path_check_failed"
	RuleInvalidPolicy            = "invalid_policy"
)

// Policy defines the final image composition contract.
type Policy struct {
	Version              string      `json:"version"`
	Gate                 string      `json:"gate"`
	BaseImage            string      `json:"base_image"`
	RequiredEntrypoint   []string    `json:"required_entrypoint"`
	AllowedExecutables   []string    `json:"allowed_executables"`
	ForbiddenBasenames   []string    `json:"forbidden_basenames"`
	AdditionalPathChecks []PathCheck `json:"additional_path_checks"`
}

// PathCheck defines an explicit filesystem assertion.
type PathCheck struct {
	Path         string `json:"path"`
	MustExist    bool   `json:"must_exist"`
	MustNotExist bool   `json:"must_not_exist"`
	ExpectedType string `json:"expected_type"`
}

// Violation is one stable JSONL diagnostic.
type Violation struct {
	Gate    string `json:"gate"`
	RuleID  string `json:"rule_id"`
	Subject string `json:"subject"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// Result contains every policy violation found in the archive.
type Result struct {
	Violations []Violation
}

// Passed reports whether the image satisfies the policy.
func (r Result) Passed() bool {
	return len(r.Violations) == 0
}

// LoadPolicy reads and validates a versioned policy.
func LoadPolicy(policyPath string) (Policy, error) {
	data, err := os.ReadFile(policyPath)
	if err != nil {
		return Policy{}, fmt.Errorf("read policy: %w", err)
	}
	var policy Policy

	if err := json.Unmarshal(data, &policy); err != nil {
		return Policy{}, fmt.Errorf("parse policy: %w", err)
	}
	if err := validatePolicy(policy); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

// Analyze validates the Dockerfile base image and the final Docker archive.
func Analyze(archive io.Reader, dockerfilePath string, policy Policy) (Result, error) {
	violations := validateDockerfile(dockerfilePath, policy)
	image, err := readDockerArchive(archive)
	if err != nil {
		return Result{}, err
	}

	violations = append(violations, validateConfig(image.Config, policy)...)
	violations = append(violations, validateRootFS(image.RootFS, policy)...)
	slices.SortFunc(violations, func(left, right Violation) int {
		if left.RuleID != right.RuleID {
			return strings.Compare(left.RuleID, right.RuleID)
		}
		return strings.Compare(left.Subject, right.Subject)
	})
	return Result{Violations: violations}, nil
}

func validatePolicy(policy Policy) error {
	if policy.Version != "1" {
		return fmt.Errorf("[%s] unsupported policy version %q", RuleInvalidPolicy, policy.Version)
	}
	if strings.TrimSpace(policy.Gate) == "" {
		return fmt.Errorf("[%s] policy gate is required", RuleInvalidPolicy)
	}
	if !isDigestReference(policy.BaseImage) {
		return fmt.Errorf("[%s] base_image must use name@sha256:digest", RuleBaseImageUnpinned)
	}
	if len(policy.RequiredEntrypoint) == 0 {
		return fmt.Errorf("[%s] required_entrypoint is required", RuleInvalidPolicy)
	}

	forbidden := stringSet(policy.ForbiddenBasenames)
	for _, executable := range policy.AllowedExecutables {
		normalized, err := normalizeAbsolute(executable)
		if err != nil {
			return fmt.Errorf("[%s] allowed executable %q: %w", RuleInvalidPolicy, executable, err)
		}
		if _, found := forbidden[path.Base(normalized)]; found {
			return fmt.Errorf(
				"[%s] allowed executable %q has forbidden basename",
				RuleBinaryDependencyDetected,
				executable,
			)
		}
	}
	for _, check := range policy.AdditionalPathChecks {
		if _, err := normalizeAbsolute(check.Path); err != nil {
			return fmt.Errorf("[%s] path check %q: %w", RuleInvalidPolicy, check.Path, err)
		}
		if check.MustExist == check.MustNotExist {
			return fmt.Errorf(
				"[%s] path check %q must set exactly one of must_exist or must_not_exist",
				RuleInvalidPolicy,
				check.Path,
			)
		}
	}
	return nil
}

func validateDockerfile(dockerfilePath string, policy Policy) []Violation {
	data, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return []Violation{{
			Gate:    policy.Gate,
			RuleID:  RuleBaseImageUnpinned,
			Subject: dockerfilePath,
			Message: fmt.Sprintf("read Dockerfile: %v", err),
		}}
	}

	finalBase := ""
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		finalBase = fields[1]
	}
	if finalBase == policy.BaseImage {
		return []Violation{}
	}
	return []Violation{{
		Gate:    policy.Gate,
		RuleID:  RuleBaseImageUnpinned,
		Subject: finalBase,
		Path:    dockerfilePath,
		Message: fmt.Sprintf("final FROM must equal policy base_image %q", policy.BaseImage),
	}}
}

func validateConfig(config imageConfig, policy Policy) []Violation {
	violations := []Violation{}
	if !slices.Equal(config.Config.Entrypoint, policy.RequiredEntrypoint) {
		violations = append(violations, Violation{
			Gate:    policy.Gate,
			RuleID:  RuleEntrypointMismatch,
			Subject: strings.Join(config.Config.Entrypoint, " "),
			Path:    "image config Entrypoint",
			Message: fmt.Sprintf("entrypoint must equal %q", policy.RequiredEntrypoint),
		})
	}

	forbidden := stringSet(policy.ForbiddenBasenames)
	for configPath, args := range map[string][]string{
		"image config Entrypoint": config.Config.Entrypoint,
		"image config Cmd":        config.Config.Cmd,
	} {
		for _, argument := range args {
			if _, found := forbidden[path.Base(argument)]; !found {
				continue
			}
			violations = append(violations, Violation{
				Gate:    policy.Gate,
				RuleID:  RuleBinaryDependencyDetected,
				Subject: path.Base(argument),
				Path:    configPath,
				Message: "forbidden basename matched in image configuration",
			})
		}
	}
	return violations
}

func validateRootFS(rootFS map[string]fileEntry, policy Policy) []Violation {
	violations := validateExecutables(rootFS, policy)
	return append(violations, validateAdditionalPaths(rootFS, policy)...)
}

func validateExecutables(rootFS map[string]fileEntry, policy Policy) []Violation {
	violations := []Violation{}
	allowed := stringSet(policy.AllowedExecutables)
	forbidden := stringSet(policy.ForbiddenBasenames)

	paths := make([]string, 0, len(rootFS))
	for filePath := range rootFS {
		paths = append(paths, filePath)
	}
	slices.Sort(paths)
	for _, filePath := range paths {
		entry := rootFS[filePath]
		if _, found := forbidden[path.Base(filePath)]; found {
			violations = append(violations, Violation{
				Gate:    policy.Gate,
				RuleID:  RuleBinaryDependencyDetected,
				Subject: path.Base(filePath),
				Path:    filePath,
				Message: "forbidden basename exists in final rootfs",
			})
		}
		if !isExecutablePath(filePath, rootFS, map[string]struct{}{}) {
			continue
		}
		if _, found := allowed[filePath]; found {
			continue
		}
		violations = append(violations, Violation{
			Gate:    policy.Gate,
			RuleID:  RuleUnexpectedExecutable,
			Subject: filePath,
			Path:    entry.Layer,
			Message: "executable file is not in policy allowed_executables",
		})
	}
	return violations
}

func validateAdditionalPaths(rootFS map[string]fileEntry, policy Policy) []Violation {
	violations := []Violation{}
	for _, check := range policy.AdditionalPathChecks {
		filePath, err := normalizeAbsolute(check.Path)
		if err != nil {
			violations = append(violations, pathViolation(policy, check.Path, err.Error()))
			continue
		}
		entry, found := rootFS[filePath]
		switch {
		case check.MustExist && !found:
			violations = append(violations, pathViolation(policy, filePath, "required path does not exist"))
		case check.MustNotExist && found:
			violations = append(violations, pathViolation(policy, filePath, "forbidden path exists"))
		case found && check.ExpectedType == "executable" && !isExecutablePath(filePath, rootFS, map[string]struct{}{}):
			violations = append(violations, pathViolation(policy, filePath, "path is not executable"))
		case found && check.ExpectedType == "regular" && entry.Type != tar.TypeReg:
			violations = append(violations, pathViolation(policy, filePath, "path is not a regular file"))
		}
	}
	return violations
}

func pathViolation(policy Policy, subject, message string) Violation {
	return Violation{
		Gate:    policy.Gate,
		RuleID:  RulePathCheckFailed,
		Subject: subject,
		Path:    subject,
		Message: message,
	}
}

type dockerManifestEntry struct {
	Config string   `json:"Config"`
	Layers []string `json:"Layers"`
}

type imageConfig struct {
	Config struct {
		Entrypoint []string `json:"Entrypoint"`
		Cmd        []string `json:"Cmd"`
	} `json:"config"`
}

type archiveMember struct {
	Data []byte
}

type imageArchive struct {
	Config imageConfig
	RootFS map[string]fileEntry
}

type fileEntry struct {
	Type     byte
	Mode     int64
	Linkname string
	Layer    string
}

func readDockerArchive(reader io.Reader) (imageArchive, error) {
	members := map[string]archiveMember{}
	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return imageArchive{}, fmt.Errorf("read image archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		data, err := io.ReadAll(tarReader)
		if err != nil {
			return imageArchive{}, fmt.Errorf("read archive member %q: %w", header.Name, err)
		}
		members[path.Clean(header.Name)] = archiveMember{Data: data}
	}

	manifestMember, found := members["manifest.json"]
	if !found {
		return imageArchive{}, fmt.Errorf("image archive missing manifest.json")
	}
	var manifest []dockerManifestEntry
	if err := json.Unmarshal(manifestMember.Data, &manifest); err != nil {
		return imageArchive{}, fmt.Errorf("parse image manifest: %w", err)
	}
	if len(manifest) != 1 {
		return imageArchive{}, fmt.Errorf("image archive must contain exactly one image, got %d", len(manifest))
	}

	configMember, found := members[path.Clean(manifest[0].Config)]
	if !found {
		return imageArchive{}, fmt.Errorf("image archive missing config %q", manifest[0].Config)
	}
	var config imageConfig
	if err := json.Unmarshal(configMember.Data, &config); err != nil {
		return imageArchive{}, fmt.Errorf("parse image config: %w", err)
	}

	rootFS := map[string]fileEntry{}
	for _, layerName := range manifest[0].Layers {
		layerMember, found := members[path.Clean(layerName)]
		if !found {
			return imageArchive{}, fmt.Errorf("image archive missing layer %q", layerName)
		}
		if err := applyLayer(rootFS, layerMember.Data, layerName); err != nil {
			return imageArchive{}, err
		}
	}
	return imageArchive{Config: config, RootFS: rootFS}, nil
}

func applyLayer(rootFS map[string]fileEntry, data []byte, layerName string) error {
	tarReader := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read layer %q: %w", layerName, err)
		}

		filePath, err := normalizeArchivePath(header.Name)
		if err != nil {
			return fmt.Errorf("layer %q path %q: %w", layerName, header.Name, err)
		}
		if filePath == "/" {
			if header.Typeflag == tar.TypeDir {
				continue
			}
			return fmt.Errorf("layer %q path %q: root entry must be a directory", layerName, header.Name)
		}
		basename := path.Base(filePath)
		directory := path.Dir(filePath)
		switch {
		case basename == ".wh..wh..opq":
			removeChildren(rootFS, directory)
			continue
		case strings.HasPrefix(basename, ".wh."):
			removePath(rootFS, path.Join(directory, strings.TrimPrefix(basename, ".wh.")))
			continue
		case header.Typeflag == tar.TypeDir:
			continue
		}

		rootFS[filePath] = fileEntry{
			Type:     header.Typeflag,
			Mode:     header.Mode,
			Linkname: header.Linkname,
			Layer:    layerName,
		}
	}
}

func removePath(rootFS map[string]fileEntry, target string) {
	delete(rootFS, target)
	removeChildren(rootFS, target)
}

func removeChildren(rootFS map[string]fileEntry, directory string) {
	prefix := strings.TrimSuffix(directory, "/") + "/"
	for filePath := range rootFS {
		if strings.HasPrefix(filePath, prefix) {
			delete(rootFS, filePath)
		}
	}
}

func isExecutablePath(
	filePath string,
	rootFS map[string]fileEntry,
	visited map[string]struct{},
) bool {
	if _, found := visited[filePath]; found {
		return false
	}
	visited[filePath] = struct{}{}

	entry, found := rootFS[filePath]
	if !found {
		return false
	}
	switch entry.Type {
	case tar.TypeReg:
		return entry.Mode&0o111 != 0
	case tar.TypeSymlink, tar.TypeLink:
		target := entry.Linkname
		if !strings.HasPrefix(target, "/") {
			target = path.Join(path.Dir(filePath), target)
		}
		target, err := normalizeAbsolute(target)
		if err != nil {
			return false
		}
		return isExecutablePath(target, rootFS, visited)
	default:
		return false
	}
}

func normalizeArchivePath(value string) (string, error) {
	trimmed := strings.TrimPrefix(value, "./")
	if trimmed == "" || trimmed == "." {
		return "/", nil
	}
	if strings.HasPrefix(trimmed, "/") || trimmed == ".." || strings.HasPrefix(trimmed, "../") {
		return "", fmt.Errorf("invalid archive path")
	}

	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("invalid archive path")
	}
	return "/" + cleaned, nil
}

func normalizeAbsolute(value string) (string, error) {
	if !strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("path must be absolute")
	}
	cleaned := path.Clean(value)
	if cleaned == "/" || strings.HasPrefix(cleaned, "/../") {
		return "", fmt.Errorf("invalid absolute path")
	}
	return cleaned, nil
}

func isDigestReference(value string) bool {
	name, digest, found := strings.Cut(value, "@sha256:")
	return found && strings.TrimSpace(name) != "" && len(digest) == 64
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

// MarshalViolation returns one compact JSONL diagnostic.
func MarshalViolation(violation Violation) ([]byte, error) {
	return json.Marshal(violation)
}

// ArchiveFile opens an archive path or returns stdin for "-".
func ArchiveFile(archivePath string, stdin io.Reader) (io.ReadCloser, error) {
	if archivePath == "-" {
		return io.NopCloser(stdin), nil
	}
	file, err := os.Open(filepath.Clean(archivePath))
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	return file, nil
}
