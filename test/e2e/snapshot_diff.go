package e2e

import (
	"fmt"
	"sort"
	"strings"
)

// SnapshotMismatchError describes a safe, entity-level snapshot difference.
// It intentionally contains no raw observations, secrets, values, addresses,
// or wrapped implementation errors.
type SnapshotMismatchError struct {
	Path     string
	Expected string
	Actual   string
}

func (e *SnapshotMismatchError) Error() string {
	if e == nil {
		return "snapshot mismatch"
	}
	path := e.Path
	if path == "" {
		path = "snapshot"
	}
	if e.Expected == "" && e.Actual == "" {
		return fmt.Sprintf("snapshot mismatch: %s", path)
	}
	return fmt.Sprintf("snapshot mismatch: %s expected %s actual %s", path, safeSnapshotValue(e.Expected), safeSnapshotValue(e.Actual))
}

// Is allows callers to use errors.Is without exposing raw expected/actual
// payloads or implementation-specific error types.
func (e *SnapshotMismatchError) Is(target error) bool {
	_, ok := target.(*SnapshotMismatchError)
	return ok
}

// SnapshotDiff is a safe, deterministic representation of one mismatch.
type SnapshotDiff struct {
	Path     string `json:"path"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
}

// AssertSnapshot compares an observed snapshot (got) with the desired
// snapshot (want) and returns a sanitized mismatch error. It does not mutate
// either argument.
func AssertSnapshot(got, want FixtureSnapshot) error {
	diffs := DiffSnapshots(want, got)
	if len(diffs) == 0 {
		return nil
	}
	first := diffs[0]
	return &SnapshotMismatchError{
		Path:     safeSnapshotPath(first.Path),
		Expected: safeSnapshotValue(first.Expected),
		Actual:   safeSnapshotValue(first.Actual),
	}
}

// CompareSnapshots is a descriptive alias for AssertSnapshot.
func CompareSnapshots(got, want FixtureSnapshot) error {
	return AssertSnapshot(got, want)
}

// DiffSnapshots returns deterministic, safe entity-level differences. Full
// payload comparison is intentionally omitted because full observations may
// contain fields outside the stable identity contract.
func DiffSnapshots(expected, actual FixtureSnapshot) []SnapshotDiff {
	diffs := make([]SnapshotDiff, 0)
	compareIdentityRefs(&diffs, "identity.customers", expected.Identity.Customers, actual.Identity.Customers)
	compareIdentityRefs(&diffs, "identity.clusters", expected.Identity.Clusters, actual.Identity.Clusters)
	compareIdentityRefs(&diffs, "identity.release_definitions", expected.Identity.ReleaseDefinitions, actual.Identity.ReleaseDefinitions)
	compareInventoryRefs(&diffs, expected.Identity.ReleaseInventories, actual.Identity.ReleaseInventories)
	compareSessions(&diffs, expected.Identity.OperatorSessions, actual.Identity.OperatorSessions)
	compareOperations(&diffs, expected.Identity.Operations, actual.Identity.Operations)
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].Path < diffs[j].Path })
	for index := range diffs {
		diffs[index].Path = safeSnapshotPath(diffs[index].Path)
		diffs[index].Expected = safeSnapshotValue(diffs[index].Expected)
		diffs[index].Actual = safeSnapshotValue(diffs[index].Actual)
	}
	return diffs
}

// SnapshotEqual is an alias for semantic snapshot equality.
func SnapshotEqual(expected, actual FixtureSnapshot) bool {
	return AssertSnapshot(actual, expected) == nil
}

func compareIdentityRefs(diffs *[]SnapshotDiff, prefix string, expected, actual []IdentityRef) {
	expectedMap := make(map[string]IdentityRef, len(expected))
	actualMap := make(map[string]IdentityRef, len(actual))
	for _, ref := range expected {
		expectedMap[ref.ID] = ref
	}
	for _, ref := range actual {
		actualMap[ref.ID] = ref
	}
	keys := sortedKeys(expectedMap, actualMap)
	for _, key := range keys {
		e, eok := expectedMap[key]
		a, aok := actualMap[key]
		path := prefix + "[" + key + "]"
		switch {
		case !eok:
			*diffs = append(*diffs, SnapshotDiff{Path: path, Actual: "present"})
		case !aok:
			*diffs = append(*diffs, SnapshotDiff{Path: path, Expected: "present"})
		case e.Name != a.Name:
			*diffs = append(*diffs, SnapshotDiff{Path: path + ".name", Expected: e.Name, Actual: a.Name})
		}
	}
}

func compareInventoryRefs(diffs *[]SnapshotDiff, expected, actual []InventoryRef) {
	expectedMap := make(map[string]InventoryRef, len(expected))
	actualMap := make(map[string]InventoryRef, len(actual))
	for _, ref := range expected {
		expectedMap[inventoryRefKey(ref)] = ref
	}
	for _, ref := range actual {
		actualMap[inventoryRefKey(ref)] = ref
	}
	keys := sortedKeys(expectedMap, actualMap)
	for _, key := range keys {
		e, eok := expectedMap[key]
		a, aok := actualMap[key]
		path := "identity.release_inventories[" + key + "]"
		switch {
		case !eok:
			*diffs = append(*diffs, SnapshotDiff{Path: path, Actual: "present"})
		case !aok:
			*diffs = append(*diffs, SnapshotDiff{Path: path, Expected: "present"})
		default:
			compareInventoryFields(diffs, path, e, a)
		}
	}
}

func compareInventoryFields(diffs *[]SnapshotDiff, path string, expected, actual InventoryRef) {
	fields := []struct {
		name     string
		expected string
		actual   string
	}{
		{"namespace", expected.Namespace, actual.Namespace},
		{"release_name", expected.ReleaseName, actual.ReleaseName},
		{"chart", expected.Chart, actual.Chart},
		{"chart_version", expected.ChartVersion, actual.ChartVersion},
		{"status", expected.Status, actual.Status},
	}
	for _, field := range fields {
		if field.expected != field.actual {
			*diffs = append(*diffs, SnapshotDiff{Path: path + "." + field.name, Expected: field.expected, Actual: field.actual})
		}
	}
	if expected.Revision != actual.Revision {
		*diffs = append(*diffs, SnapshotDiff{Path: path + ".revision", Expected: fmt.Sprint(expected.Revision), Actual: fmt.Sprint(actual.Revision)})
	}
	if expected.SnapshotVersion != actual.SnapshotVersion {
		*diffs = append(*diffs, SnapshotDiff{Path: path + ".snapshot_version", Expected: fmt.Sprint(expected.SnapshotVersion), Actual: fmt.Sprint(actual.SnapshotVersion)})
	}
}

func compareSessions(diffs *[]SnapshotDiff, expected, actual []SessionRef) {
	expectedMap := make(map[string]string, len(expected))
	actualMap := make(map[string]string, len(actual))
	for _, ref := range expected {
		expectedMap[ref.OperatorID] = ref.Status
	}
	for _, ref := range actual {
		actualMap[ref.OperatorID] = ref.Status
	}
	for _, key := range sortedKeys(expectedMap, actualMap) {
		e, eok := expectedMap[key]
		a, aok := actualMap[key]
		path := "identity.operator_sessions[" + key + "]"
		switch {
		case !eok:
			*diffs = append(*diffs, SnapshotDiff{Path: path, Actual: "present"})
		case !aok:
			*diffs = append(*diffs, SnapshotDiff{Path: path, Expected: "present"})
		case e != a:
			*diffs = append(*diffs, SnapshotDiff{Path: path + ".status", Expected: e, Actual: a})
		}
	}
}

func compareOperations(diffs *[]SnapshotDiff, expected, actual []OperationSummary) {
	expectedMap := make(map[string]OperationSummary, len(expected))
	actualMap := make(map[string]OperationSummary, len(actual))
	for _, operation := range expected {
		expectedMap[operation.ID] = operation
	}
	for _, operation := range actual {
		actualMap[operation.ID] = operation
	}
	for _, key := range sortedKeys(expectedMap, actualMap) {
		e, eok := expectedMap[key]
		a, aok := actualMap[key]
		path := "identity.operations[" + key + "]"
		switch {
		case !eok:
			*diffs = append(*diffs, SnapshotDiff{Path: path, Actual: "present"})
		case !aok:
			*diffs = append(*diffs, SnapshotDiff{Path: path, Expected: "present"})
		default:
			if e.Type != a.Type {
				*diffs = append(*diffs, SnapshotDiff{Path: path + ".type", Expected: e.Type, Actual: a.Type})
			}
			if e.State != a.State {
				*diffs = append(*diffs, SnapshotDiff{Path: path + ".state", Expected: e.State, Actual: a.State})
			}
			if e.StateVersion != a.StateVersion {
				*diffs = append(*diffs, SnapshotDiff{Path: path + ".state_version", Expected: fmt.Sprint(e.StateVersion), Actual: fmt.Sprint(a.StateVersion)})
			}
		}
	}
}

func sortedKeys(expected, actual any) []string {
	keys := make(map[string]struct{})
	add := func(value any) {
		switch values := value.(type) {
		case map[string]IdentityRef:
			for key := range values {
				keys[key] = struct{}{}
			}
		case map[string]InventoryRef:
			for key := range values {
				keys[key] = struct{}{}
			}
		case map[string]string:
			for key := range values {
				keys[key] = struct{}{}
			}
		case map[string]OperationSummary:
			for key := range values {
				keys[key] = struct{}{}
			}
		}
	}
	add(expected)
	add(actual)
	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func safeSnapshotValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "<empty>"
	}
	if len(value) > 64 {
		return "<redacted>"
	}
	if strings.ContainsAny(value, "\r\n\t") {
		return "<redacted>"
	}
	for _, token := range []string{"jwt", "token", "secret", "password", "values", "postgres", "mysql", "://"} {
		if strings.Contains(strings.ToLower(value), token) {
			return "<redacted>"
		}
	}
	return value
}

func safeSnapshotPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || len(path) > 256 || strings.ContainsAny(path, "\r\n\t") {
		return "snapshot"
	}
	lowerPath := strings.ToLower(path)
	for _, token := range []string{"jwt", "token", "secret", "password", "values", "postgres", "mysql", "://"} {
		if strings.Contains(lowerPath, token) {
			return "snapshot"
		}
	}
	return path
}

var _ error = (*SnapshotMismatchError)(nil)
