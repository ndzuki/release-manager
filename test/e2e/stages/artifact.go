package stages

import (
	"context"
	"fmt"
	"strings"

	e2e "github.com/ndzuki/release-manager/test/e2e"
)

const (
	// BundleStatusValidated is the normalized public bundle lifecycle status.
	BundleStatusValidated = "validated"
)

// BundleRequest contains the public identifiers accepted by BundleService.GetBundle.
type BundleRequest struct {
	BundleID            string
	ReleaseDefinitionID string
}

// BundleReader is the consumer-side seam for the read-only GetBundle route.
type BundleReader interface {
	GetBundle(context.Context, BundleRequest) (BundleObservation, error)
}

// ArtifactReader combines the read-only bundle and inventory routes used by
// the artifact stage. The generated Connect clients stay behind this seam.
type ArtifactReader interface {
	BundleReader
	InventoryReader
}

// RouteExpectation describes the optional route identity attached to a bundle
// observation. The current ListReleaseInventory RPC exposes release rows but
// not dedicated route fields, so unset fields are intentionally not asserted.
type RouteExpectation struct {
	ID           string
	CustomerID   string
	ClusterID    string
	DefinitionID string
	Namespace    string
	SourcePrefix string
}

// ArtifactExpectation contains the seed-derived bundle and route assertions.
type ArtifactExpectation struct {
	BundleID            string
	Digest              string
	ReleaseDefinitionID string
	Route               RouteExpectation
}

// ArtifactStage validates a bundle through BundleService.GetBundle and checks
// the associated release identity through ListReleaseInventory. It performs no
// writes and never creates or mutates a bundle.
type ArtifactStage struct {
	reader      ArtifactReader
	expectation ArtifactExpectation
	artifacts   []e2e.ArtifactRef
	bundle      BundleObservation
	row         InventoryRow
}

var _ e2e.Stage = (*ArtifactStage)(nil)

// NewArtifactStage creates a read-only artifact observation stage.
func NewArtifactStage(reader ArtifactReader, expectation ArtifactExpectation) *ArtifactStage {
	return &ArtifactStage{reader: reader, expectation: expectation}
}

// NewArtifact is a concise alias for NewArtifactStage.
func NewArtifact(reader ArtifactReader, expectation ArtifactExpectation) *ArtifactStage {
	return NewArtifactStage(reader, expectation)
}

// Name implements e2e.Stage.
func (s *ArtifactStage) Name() string { return "artifact" }

// Run implements e2e.Stage.
func (s *ArtifactStage) Run(ctx context.Context, _ *e2e.Fixture) error {
	if s == nil || s.reader == nil {
		return newStageError(CodeSnapshotNotFound, "artifact", "artifact observation unavailable")
	}
	bundle, err := s.reader.GetBundle(ctx, BundleRequest{
		BundleID:            s.expectation.BundleID,
		ReleaseDefinitionID: s.expectation.ReleaseDefinitionID,
	})
	if err != nil {
		return newStageError(CodeSnapshotNotFound, "bundle", "bundle observation failed")
	}
	if err := validateBundle(bundle, s.expectation); err != nil {
		return err
	}
	inventory, err := s.reader.ListReleaseInventory(ctx)
	if err != nil {
		return newStageError(CodeSnapshotNotFound, "inventory", "route observation failed")
	}
	row, err := findArtifactRow(inventory, s.expectation)
	if err != nil {
		return err
	}
	if err := validateRoute(row, inventory.Routes, s.expectation.Route); err != nil {
		return err
	}

	s.bundle = bundle
	s.row = row
	s.artifacts = []e2e.ArtifactRef{{
		ID:     bundle.ID,
		Digest: bundle.Digest,
	}}
	return nil
}

// Artifacts returns the stable artifact references from the last successful
// observation.
func (s *ArtifactStage) Artifacts() []e2e.ArtifactRef {
	if s == nil {
		return nil
	}
	return append([]e2e.ArtifactRef(nil), s.artifacts...)
}

// Bundle returns the last validated public bundle observation.
func (s *ArtifactStage) Bundle() BundleObservation {
	if s == nil {
		return BundleObservation{}
	}
	return s.bundle
}

// Row returns the last validated release inventory row.
func (s *ArtifactStage) Row() InventoryRow {
	if s == nil {
		return InventoryRow{}
	}
	return s.row
}

func validateBundle(bundle BundleObservation, expectation ArtifactExpectation) error {
	if strings.TrimSpace(bundle.ID) == "" {
		return newStageError(CodeSnapshotNotFound, "bundle", "bundle identity missing")
	}
	if expectation.BundleID != "" && bundle.ID != expectation.BundleID {
		return newStageError(CodeFixtureStale, "bundle.id", fmt.Sprintf("expected %q got %q", expectation.BundleID, bundle.ID))
	}
	if strings.TrimSpace(bundle.Digest) == "" {
		return newStageError(CodeSnapshotNotFound, "bundle.digest", "bundle digest missing")
	}
	if expectation.Digest != "" && bundle.Digest != expectation.Digest {
		return newStageError(CodeFixtureStale, "bundle.digest", fmt.Sprintf("expected %q got %q", expectation.Digest, bundle.Digest))
	}
	if !isValidatedBundleStatus(bundle.Status) {
		return newStageError(CodeEnvironmentUnhealthy, "bundle.status", "bundle is not validated")
	}
	return nil
}

func isValidatedBundleStatus(status string) bool {
	normalized := strings.ToLower(strings.TrimSpace(status))
	normalized = strings.TrimPrefix(normalized, "bundle_status_")
	normalized = strings.TrimPrefix(normalized, "bundle_status")
	normalized = strings.TrimPrefix(normalized, "bundle_status_")
	return normalized == BundleStatusValidated || strings.HasSuffix(normalized, "_validated")
}

func findArtifactRow(observation InventoryObservation, expectation ArtifactExpectation) (InventoryRow, error) {
	for _, row := range observation.Rows {
		if expectation.ReleaseDefinitionID != "" && row.DefinitionID != expectation.ReleaseDefinitionID {
			continue
		}
		if expectation.Route.CustomerID != "" && row.CustomerID != expectation.Route.CustomerID {
			continue
		}
		if expectation.Route.ClusterID != "" && row.ClusterID != expectation.Route.ClusterID {
			continue
		}
		if expectation.Route.Namespace != "" && row.Namespace != expectation.Route.Namespace {
			continue
		}
		return row, nil
	}
	if expectation.ReleaseDefinitionID == "" {
		return InventoryRow{}, newStageError(CodeSnapshotNotFound, "inventory", "release inventory row missing")
	}
	return InventoryRow{}, newStageError(CodeFixtureStale, "inventory.definition_id", fmt.Sprintf("expected %q got %q", expectation.ReleaseDefinitionID, "missing"))
}

// routeExpectationEmpty reports whether the expectation states no constraint at
// all, in which case there is nothing to assert.
func routeExpectationEmpty(expectation RouteExpectation) bool {
	return expectation.ID == "" && expectation.CustomerID == "" && expectation.ClusterID == "" &&
		expectation.DefinitionID == "" && expectation.Namespace == "" && expectation.SourcePrefix == ""
}

// validateRouteRow checks the inventory row's route fields, skipping every
// constraint the expectation leaves open. The check order is fixed so the
// reported field is stable.
func validateRouteRow(row InventoryRow, expectation RouteExpectation) error {
	for _, field := range []struct {
		name     string
		expected string
		actual   string
	}{
		{"route.definition_id", expectation.DefinitionID, row.DefinitionID},
		{"route.customer_id", expectation.CustomerID, row.CustomerID},
		{"route.cluster_id", expectation.ClusterID, row.ClusterID},
		{"route.namespace", expectation.Namespace, row.Namespace},
	} {
		if field.expected != "" && field.actual != field.expected {
			return newStageError(CodeFixtureStale, field.name, fmt.Sprintf("expected %q got %q", field.expected, field.actual))
		}
	}
	return nil
}

// routeSatisfies reports whether one observed route satisfies every constraint
// the expectation states; open constraints are not checked.
func routeSatisfies(route RouteObservation, expectation RouteExpectation) bool {
	for _, constraint := range []struct{ expected, actual string }{
		{expectation.ID, route.ID},
		{expectation.CustomerID, route.CustomerID},
		{expectation.ClusterID, route.ClusterID},
		{expectation.DefinitionID, route.DefinitionID},
		{expectation.Namespace, route.Namespace},
		{expectation.SourcePrefix, route.SourcePrefix},
	} {
		if constraint.expected != "" && constraint.actual != constraint.expected {
			return false
		}
	}
	return true
}

func validateRoute(row InventoryRow, routes []RouteObservation, expectation RouteExpectation) error {
	if routeExpectationEmpty(expectation) {
		return nil
	}
	if err := validateRouteRow(row, expectation); err != nil {
		return err
	}
	if len(routes) == 0 {
		// Route fields are optional in the current ListReleaseInventory RPC.
		return nil
	}
	for index := range routes {
		if routeSatisfies(routes[index], expectation) {
			return nil
		}
	}
	return newStageError(CodeFixtureStale, "route", "expected route observation missing")
}
