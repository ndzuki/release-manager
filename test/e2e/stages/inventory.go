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

// e2eDefinitionSet returns the seeded E2E definition ids as a set.
func e2eDefinitionSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// observedDefinitionIDs returns the observed definition ids, dropping blanks.
func observedDefinitionIDs(definitions []DefinitionObservation) map[string]struct{} {
	ids := make(map[string]struct{}, len(definitions))
	for index := range definitions {
		if definitions[index].ID != "" {
			ids[definitions[index].ID] = struct{}{}
		}
	}
	return ids
}

// observedRouteIDs returns the observed route ids together with any definition
// id a route carries, so a binding can be checked from either side.
func observedRouteIDs(routes []RouteObservation) map[string]struct{} {
	ids := make(map[string]struct{}, len(routes))
	for index := range routes {
		if routes[index].ID != "" {
			ids[routes[index].ID] = struct{}{}
		}
		if routes[index].DefinitionID != "" {
			ids[routes[index].DefinitionID] = struct{}{}
		}
	}
	return ids
}

// basicEntityCounts counts the observed definitions and routes that are not part
// of the seeded E2E definition set — the fixture's "basic" entities.
//
// Routes are cluster-scoped artifact routes, not definition-scoped entities:
// ClusterRoute carries no release-definition identity, so every observed route
// counts as basic. The count assertion is therefore exact even though the
// observation cannot attribute a route to a definition.
func basicEntityCounts(
	observation InventoryObservation,
	routes []RouteObservation,
	e2eDefinitions map[string]struct{},
) (basicDefinitions, basicRoutes int) {
	for index := range observation.Definitions {
		if _, ok := e2eDefinitions[observation.Definitions[index].ID]; !ok {
			basicDefinitions++
		}
	}
	for index := range routes {
		if _, ok := e2eDefinitions[routes[index].DefinitionID]; !ok {
			basicRoutes++
		}
	}
	return basicDefinitions, basicRoutes
}

// missingIdentityDifferences reports every expected E2E definition id the
// observation does not carry, plus each missing route binding when the observed
// surface expresses bindings at all (see routesBound at the call site).
func missingIdentityDifferences(
	expected []string,
	definitionIDs, routeIDs map[string]struct{},
	routesBound bool,
) []IdentityDifference {
	differences := make([]IdentityDifference, 0)
	for _, id := range expected {
		if _, ok := definitionIDs[id]; !ok {
			differences = append(differences, IdentityDifference{
				Entity:   "definition",
				Field:    "id",
				Expected: id,
				Actual:   "missing",
			})
		}
		if !routesBound {
			continue
		}
		if _, ok := routeIDs[id]; !ok {
			differences = append(differences, IdentityDifference{
				Entity:   "route",
				Field:    "definition_id",
				Expected: id,
				Actual:   "missing",
			})
		}
	}
	return differences
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

	e2eDefinitions := e2eDefinitionSet(expected.E2EDefinitionIDs)
	basicDefinitions, basicRoutes := basicEntityCounts(observation, routes, e2eDefinitions)
	appendCountDifference("definitions", "basic_count", expected.DefinitionsBasic, basicDefinitions)
	appendCountDifference("routes", "basic_count", expected.RoutesBasic, basicRoutes)

	// ClusterRoute carries no release-definition identity, so an adapter cannot
	// attribute a route to a definition and asserting the binding would fail on
	// every deployment regardless of the fixture. The requirement is applied
	// only when the observed surface actually expresses a binding: that keeps it
	// meaningful if the RPC ever gains the field, while refusing to invent an
	// attribution the API cannot supply.
	differences = append(differences, missingIdentityDifferences(
		expected.E2EDefinitionIDs,
		observedDefinitionIDs(observation.Definitions),
		observedRouteIDs(routes),
		routesCarryDefinitionIdentity(routes),
	)...)
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
