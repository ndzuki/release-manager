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

func validateRoute(row InventoryRow, routes []RouteObservation, expectation RouteExpectation) error {
	if expectation.ID == "" && expectation.CustomerID == "" && expectation.ClusterID == "" && expectation.DefinitionID == "" && expectation.Namespace == "" && expectation.SourcePrefix == "" {
		return nil
	}
	if expectation.DefinitionID != "" && row.DefinitionID != expectation.DefinitionID {
		return newStageError(CodeFixtureStale, "route.definition_id", fmt.Sprintf("expected %q got %q", expectation.DefinitionID, row.DefinitionID))
	}
	if expectation.CustomerID != "" && row.CustomerID != expectation.CustomerID {
		return newStageError(CodeFixtureStale, "route.customer_id", fmt.Sprintf("expected %q got %q", expectation.CustomerID, row.CustomerID))
	}
	if expectation.ClusterID != "" && row.ClusterID != expectation.ClusterID {
		return newStageError(CodeFixtureStale, "route.cluster_id", fmt.Sprintf("expected %q got %q", expectation.ClusterID, row.ClusterID))
	}
	if expectation.Namespace != "" && row.Namespace != expectation.Namespace {
		return newStageError(CodeFixtureStale, "route.namespace", fmt.Sprintf("expected %q got %q", expectation.Namespace, row.Namespace))
	}
	if len(routes) == 0 {
		// Route fields are optional in the current ListReleaseInventory RPC.
		return nil
	}
	for _, route := range routes {
		if expectation.ID != "" && route.ID != expectation.ID {
			continue
		}
		if expectation.CustomerID != "" && route.CustomerID != expectation.CustomerID {
			continue
		}
		if expectation.ClusterID != "" && route.ClusterID != expectation.ClusterID {
			continue
		}
		if expectation.DefinitionID != "" && route.DefinitionID != expectation.DefinitionID {
			continue
		}
		if expectation.Namespace != "" && route.Namespace != expectation.Namespace {
			continue
		}
		if expectation.SourcePrefix != "" && route.SourcePrefix != expectation.SourcePrefix {
			continue
		}
		return nil
	}
	return newStageError(CodeFixtureStale, "route", "expected route observation missing")
}
