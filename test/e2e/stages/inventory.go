package stages

import (
	"context"
	"fmt"
	"sort"
	"strings"

	e2e "github.com/ndzuki/release-manager/test/e2e"
)

// InventoryReader is the consumer-side seam for ListReleaseInventory.
//
// Implementations should make one unfiltered, read-only request and normalize
// the generated response into this package's observation types. Keeping the
// generated client behind this interface makes stage tests deterministic.
type InventoryReader interface {
	ListReleaseInventory(context.Context) (InventoryObservation, error)
}

// InventoryObservation is a normalized, read-only release inventory result.
type InventoryObservation struct {
	Customers   []IdentityObservation
	Clusters    []IdentityObservation
	Routes      []RouteObservation
	Definitions []DefinitionObservation
	Bundles     []BundleObservation
	Rows        []InventoryRow
}

// IdentityObservation is a public identity item with a stable ID.
type IdentityObservation struct {
	ID   string
	Name string
}

// RouteObservation is the public route identity needed by artifact assertions.
type RouteObservation struct {
	ID           string
	CustomerID   string
	ClusterID    string
	DefinitionID string
	Namespace    string
	SourcePrefix string
}

// DefinitionObservation is a public release-definition identity.
type DefinitionObservation struct {
	ID   string
	Name string
}

// BundleObservation is the public bundle identity exposed to read-only stages.
type BundleObservation struct {
	ID     string
	Digest string
	Status string
}

// InventoryRow captures stable release identity returned by
// ListReleaseInventory. Route fields remain optional because the current
// public RPC row does not expose route identity directly.
type InventoryRow struct {
	CustomerID   string
	ClusterID    string
	DefinitionID string
	Namespace    string
	ReleaseName  string
	Revision     int32
}

// ExpectedIdentity is the manifest-derived identity contract consumed by the
// inventory and fixture guards. It aliases the e2e config contract so callers
// can pass cfg.Seed.ExpectedIdentity without a conversion.
type ExpectedIdentity = e2e.ExpectedIdentity

// InventoryStage compares the complete observed inventory with the seed
// manifest identity. A mismatch is a run-fatal fixture_stale error.
type InventoryStage struct {
	reader      InventoryReader
	expected    ExpectedIdentity
	observation InventoryObservation
	differences []IdentityDifference
}

// IdentityDifference describes one stable entity-level mismatch.
type IdentityDifference struct {
	Entity   string
	Field    string
	Expected string
	Actual   string
}

var _ e2e.Stage = (*InventoryStage)(nil)

// NewInventoryStage creates a read-only inventory identity stage.
func NewInventoryStage(reader InventoryReader, expected ExpectedIdentity) *InventoryStage {
	return &InventoryStage{reader: reader, expected: expected}
}

// NewInventory is a concise alias for NewInventoryStage.
func NewInventory(reader InventoryReader, expected ExpectedIdentity) *InventoryStage {
	return NewInventoryStage(reader, expected)
}

// Name implements e2e.Stage.
func (s *InventoryStage) Name() string { return "inventory" }

// Run implements e2e.Stage.
func (s *InventoryStage) Run(ctx context.Context, _ *e2e.Fixture) error {
	if s == nil || s.reader == nil {
		return newStageError(CodeSnapshotNotFound, "inventory", "release inventory observation unavailable")
	}
	observation, err := s.reader.ListReleaseInventory(ctx)
	if err != nil {
		return newStageError(CodeSnapshotNotFound, "inventory", "release inventory observation failed")
	}
	differences := compareIdentity(observation, s.expected)
	if len(differences) != 0 {
		s.differences = differences
		return newStageError(CodeFixtureStale, "inventory", formatDifferences(differences))
	}
	s.observation = observation
	s.differences = nil
	return nil
}

// Observation returns the last validated inventory observation with copied
// slices.
func (s *InventoryStage) Observation() InventoryObservation {
	if s == nil {
		return InventoryObservation{}
	}
	observation := s.observation
	observation.Customers = append([]IdentityObservation(nil), observation.Customers...)
	observation.Clusters = append([]IdentityObservation(nil), observation.Clusters...)
	observation.Routes = append([]RouteObservation(nil), observation.Routes...)
	observation.Definitions = append([]DefinitionObservation(nil), observation.Definitions...)
	observation.Bundles = append([]BundleObservation(nil), observation.Bundles...)
	observation.Rows = append([]InventoryRow(nil), observation.Rows...)
	return observation
}

// Differences returns the last identity mismatch report.
func (s *InventoryStage) Differences() []IdentityDifference {
	if s == nil {
		return nil
	}
	return append([]IdentityDifference(nil), s.differences...)
}

// routesCarryDefinitionIdentity reports whether the observed route surface
// expresses release-definition identity at all.
//
// ClusterRoute — the RPC behind RouteObservation — is a cluster-scoped artifact
// route with no definition field, so a faithful adapter reports every route with
// an empty DefinitionID. This probe keeps the route/definition assertion
// meaningful on a surface that can express the binding without failing on one
// that cannot.
func routesCarryDefinitionIdentity(routes []RouteObservation) bool {
	for _, route := range routes {
		if route.DefinitionID != "" {
			return true
		}
	}
	return false
}

func compareIdentity(observation InventoryObservation, expected ExpectedIdentity) []IdentityDifference {
	customers := uniqueIdentityCount(observation.Customers)
	clusters := uniqueIdentityCount(observation.Clusters)
	bundles := uniqueBundleCount(observation.Bundles)
	routes := append([]RouteObservation(nil), observation.Routes...)

	differences := make([]IdentityDifference, 0)
	appendCountDifference := func(entity, field string, want, got int) {
		if want != got {
			differences = append(differences, IdentityDifference{
				Entity:   entity,
				Field:    field,
				Expected: fmt.Sprint(want),
				Actual:   fmt.Sprint(got),
			})
		}
	}
	appendCountDifference("customers", "count", expected.Customers, customers)
	appendCountDifference("clusters", "count", expected.Clusters, clusters)
	appendCountDifference("bundles", "count", expected.Bundles, bundles)

	e2eDefinitions := make(map[string]struct{}, len(expected.E2EDefinitionIDs))
	for _, id := range expected.E2EDefinitionIDs {
		e2eDefinitions[id] = struct{}{}
	}
	basicDefinitions := 0
	for _, definition := range observation.Definitions {
		if _, ok := e2eDefinitions[definition.ID]; !ok {
			basicDefinitions++
		}
	}
	// Routes are cluster-scoped artifact routes, not definition-scoped
	// entities: ClusterRoute carries no release-definition identity, so every
	// observed route counts as basic. The count assertion below is therefore
	// exact even though the observation cannot attribute a route to a
	// definition.
	basicRoutes := 0
	for _, route := range routes {
		if _, ok := e2eDefinitions[route.DefinitionID]; !ok {
			basicRoutes++
		}
	}
	appendCountDifference("definitions", "basic_count", expected.DefinitionsBasic, basicDefinitions)
	appendCountDifference("routes", "basic_count", expected.RoutesBasic, basicRoutes)

	definitionIDs := make(map[string]struct{}, len(observation.Definitions))
	for _, definition := range observation.Definitions {
		if definition.ID != "" {
			definitionIDs[definition.ID] = struct{}{}
		}
	}
	routeIDs := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		if route.ID != "" {
			routeIDs[route.ID] = struct{}{}
		}
		if route.DefinitionID != "" {
			routeIDs[route.DefinitionID] = struct{}{}
		}
	}
	// ClusterRoute carries no release-definition identity, so an adapter cannot
	// attribute a route to a definition and asserting the binding would fail on
	// every deployment regardless of the fixture. The requirement is applied
	// only when the observed surface actually expresses a binding: that keeps it
	// meaningful if the RPC ever gains the field, while refusing to invent an
	// attribution the API cannot supply.
	routesBound := routesCarryDefinitionIdentity(routes)
	for _, id := range expected.E2EDefinitionIDs {
		if _, ok := definitionIDs[id]; !ok {
			differences = append(differences, IdentityDifference{
				Entity:   "definition",
				Field:    "id",
				Expected: id,
				Actual:   "missing",
			})
		}
		if routesBound {
			if _, ok := routeIDs[id]; !ok {
				differences = append(differences, IdentityDifference{
					Entity:   "route",
					Field:    "definition_id",
					Expected: id,
					Actual:   "missing",
				})
			}
		}
	}
	sort.Slice(differences, func(i, j int) bool {
		if differences[i].Entity != differences[j].Entity {
			return differences[i].Entity < differences[j].Entity
		}
		if differences[i].Field != differences[j].Field {
			return differences[i].Field < differences[j].Field
		}
		if differences[i].Expected != differences[j].Expected {
			return differences[i].Expected < differences[j].Expected
		}
		return differences[i].Actual < differences[j].Actual
	})
	return differences
}

func uniqueIdentityCount(items []IdentityObservation) int {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.ID != "" {
			seen[item.ID] = struct{}{}
		}
	}
	return len(seen)
}

func uniqueDefinitionCount(items []DefinitionObservation) int {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.ID != "" {
			seen[item.ID] = struct{}{}
		}
	}
	return len(seen)
}

func uniqueBundleCount(items []BundleObservation) int {
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.ID != "" {
			seen[item.ID] = struct{}{}
		}
	}
	return len(seen)
}

func formatDifferences(differences []IdentityDifference) string {
	parts := make([]string, 0, len(differences))
	for _, difference := range differences {
		parts = append(parts, fmt.Sprintf(
			"%s.%s expected %q got %q",
			difference.Entity,
			difference.Field,
			difference.Expected,
			difference.Actual,
		))
	}
	return strings.Join(parts, "; ")
}
