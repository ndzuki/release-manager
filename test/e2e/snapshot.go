package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"
)

// FixtureSnapshot is a read-only observation of the public E2E fixture state.
// Full contains only the fixed, public response projection selected by the
// caller; credentials, values, tokens, connection strings, and internal IPs
// are never part of a snapshot.
type FixtureSnapshot struct {
	CollectedAt time.Time        `json:"collected_at"`
	Identity    SnapshotIdentity `json:"identity"`
	Full        json.RawMessage  `json:"full,omitempty"`
}

// SnapshotIdentity is the stable identity projection used by fixture guards.
type SnapshotIdentity struct {
	Customers          []IdentityRef      `json:"customers"`
	Clusters           []IdentityRef      `json:"clusters"`
	ReleaseDefinitions []IdentityRef      `json:"release_definitions"`
	ReleaseInventories []InventoryRef     `json:"release_inventories,omitempty"`
	OperatorSessions   []SessionRef       `json:"operator_sessions"`
	Operations         []OperationSummary `json:"operations"`
}

// IdentityRef is a public entity identity. Name is optional for entities whose
// public observation has no display name.
type IdentityRef struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// InventoryRef is the public release-inventory observation. It intentionally
// carries no values, secrets, workload UID, or internal sync identifiers.
type InventoryRef struct {
	ReleaseDefinitionID string `json:"release_definition_id"`
	CustomerID          string `json:"customer_id"`
	ClusterID           string `json:"cluster_id"`
	Namespace           string `json:"namespace"`
	ReleaseName         string `json:"release_name"`
	Chart               string `json:"chart"`
	ChartVersion        string `json:"chart_version"`
	Revision            int    `json:"revision"`
	Status              string `json:"status"`
	SnapshotVersion     int64  `json:"snapshot_version"`
}

// SessionRef is the public operator-session observation.
type SessionRef struct {
	OperatorID string `json:"operator_id"`
	Status     string `json:"status"`
}

// OperationSummary is the public operation identity and state observation.
type OperationSummary struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	State        string `json:"state"`
	StateVersion int64  `json:"state_version"`
}

// Clone returns a deep copy that shares no slice or RawMessage backing data
// with the receiver. A nil receiver remains nil.
func (s *FixtureSnapshot) Clone() *FixtureSnapshot {
	if s == nil {
		return nil
	}
	clone := *s
	clone.Identity = s.Identity.clone()
	clone.Full = cloneRawJSON(s.Full)
	return &clone
}

// DeepCopy is an explicit alias for Clone for callers that use deep-copy
// terminology in observation seams.
func (s *FixtureSnapshot) DeepCopy() *FixtureSnapshot { return s.Clone() }

// Immutable returns a detached snapshot value suitable for handing to stages.
// Callers cannot mutate the source snapshot through its slices or RawMessage.
func (s FixtureSnapshot) Immutable() FixtureSnapshot {
	clone := s.Clone()
	if clone == nil {
		return FixtureSnapshot{}
	}
	return *clone
}

// StableJSON returns deterministic compact JSON. encoding/json emits struct
// fields in declaration order and sorts map keys, so the result is stable
// without a lossy interface{} round trip for large integers.
func StableJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal stable json: %w", err)
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("marshal stable json: invalid json")
	}
	return data, nil
}

// StableDigest returns the SHA-256 digest of deterministic compact JSON. The
// result uses the project-wide sha256:<lowercase-hex> representation.
func StableDigest(value any) (string, error) {
	data, err := StableJSON(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// SnapshotDigest returns the stable digest of a snapshot after normalizing
// public identity collection ordering.
func SnapshotDigest(snapshot FixtureSnapshot) (string, error) {
	return StableSnapshotDigest(snapshot)
}

// Digest is a method form of SnapshotDigest.
func (s FixtureSnapshot) Digest() (string, error) { return SnapshotDigest(s) }

func (i SnapshotIdentity) clone() SnapshotIdentity {
	i.Customers = cloneIdentityRefs(i.Customers)
	i.Clusters = cloneIdentityRefs(i.Clusters)
	i.ReleaseDefinitions = cloneIdentityRefs(i.ReleaseDefinitions)
	i.ReleaseInventories = cloneInventoryRefs(i.ReleaseInventories)
	i.OperatorSessions = cloneSessionRefs(i.OperatorSessions)
	i.Operations = cloneOperationSummaries(i.Operations)
	return i
}

func cloneIdentityRefs(refs []IdentityRef) []IdentityRef {
	if refs == nil {
		return nil
	}
	clone := make([]IdentityRef, len(refs))
	copy(clone, refs)
	return clone
}

func cloneInventoryRefs(refs []InventoryRef) []InventoryRef {
	if refs == nil {
		return nil
	}
	clone := make([]InventoryRef, len(refs))
	copy(clone, refs)
	return clone
}

func cloneSessionRefs(refs []SessionRef) []SessionRef {
	if refs == nil {
		return nil
	}
	clone := make([]SessionRef, len(refs))
	copy(clone, refs)
	return clone
}

func cloneOperationSummaries(operations []OperationSummary) []OperationSummary {
	if operations == nil {
		return nil
	}
	clone := make([]OperationSummary, len(operations))
	copy(clone, operations)
	return clone
}

func cloneRawJSON(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	clone := make(json.RawMessage, len(raw))
	copy(clone, raw)
	return clone
}

// normalizeSnapshot sorts all identity collections by their stable public key
// before serializing. It returns a detached value and never mutates its input.
func normalizeSnapshot(snapshot FixtureSnapshot) FixtureSnapshot {
	copy := snapshot.Immutable()
	sort.Slice(copy.Identity.Customers, func(i, j int) bool {
		return identityRefKey(copy.Identity.Customers[i]) < identityRefKey(copy.Identity.Customers[j])
	})
	sort.Slice(copy.Identity.Clusters, func(i, j int) bool {
		return identityRefKey(copy.Identity.Clusters[i]) < identityRefKey(copy.Identity.Clusters[j])
	})
	sort.Slice(copy.Identity.ReleaseDefinitions, func(i, j int) bool {
		return identityRefKey(copy.Identity.ReleaseDefinitions[i]) < identityRefKey(copy.Identity.ReleaseDefinitions[j])
	})
	sort.Slice(copy.Identity.ReleaseInventories, func(i, j int) bool {
		return inventoryRefKey(copy.Identity.ReleaseInventories[i]) < inventoryRefKey(copy.Identity.ReleaseInventories[j])
	})
	sort.Slice(copy.Identity.OperatorSessions, func(i, j int) bool {
		return copy.Identity.OperatorSessions[i].OperatorID < copy.Identity.OperatorSessions[j].OperatorID
	})
	sort.Slice(copy.Identity.Operations, func(i, j int) bool {
		return copy.Identity.Operations[i].ID < copy.Identity.Operations[j].ID
	})
	return copy
}

func identityRefKey(ref IdentityRef) string { return ref.ID + "\x00" + ref.Name }

func inventoryRefKey(ref InventoryRef) string {
	return ref.ReleaseDefinitionID + "\x00" + ref.CustomerID + "\x00" + ref.ClusterID + "\x00" + ref.Namespace + "\x00" + ref.ReleaseName
}

// StableSnapshotJSON returns deterministic JSON with identity collections
// sorted by stable public identity.
func StableSnapshotJSON(snapshot FixtureSnapshot) ([]byte, error) {
	normalized := normalizeSnapshot(snapshot)
	if len(normalized.Full) > 0 {
		var full any
		decoder := json.NewDecoder(bytes.NewReader(normalized.Full))
		decoder.UseNumber()
		if err := decoder.Decode(&full); err != nil {
			return nil, fmt.Errorf("normalize snapshot full: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("normalize snapshot full: multiple json values")
		}
		fullData, err := json.Marshal(full)
		if err != nil {
			return nil, fmt.Errorf("marshal snapshot full: %w", err)
		}
		normalized.Full = fullData
	}
	return StableJSON(normalized)
}

// StableSnapshotDigest returns a digest of normalized, sorted snapshot data.
func StableSnapshotDigest(snapshot FixtureSnapshot) (string, error) {
	data, err := StableSnapshotJSON(snapshot)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// EqualSnapshot reports semantic equality after stable sorting and JSON
// normalization. It is intended for read-only observations, not live clients.
func EqualSnapshot(left, right FixtureSnapshot) bool {
	leftJSON, leftErr := StableSnapshotJSON(left)
	rightJSON, rightErr := StableSnapshotJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
