package stages

import (
	"context"
	"errors"
	"testing"
)

type inventoryFake struct {
	observation InventoryObservation
	err         error
}

func (f inventoryFake) ListReleaseInventory(context.Context) (InventoryObservation, error) {
	return f.observation, f.err
}

func completeInventory() InventoryObservation {
	return InventoryObservation{
		Customers: []IdentityObservation{{ID: "customer-a"}, {ID: "customer-b"}},
		Clusters:  []IdentityObservation{{ID: "cluster-a"}, {ID: "cluster-b"}, {ID: "cluster-c"}, {ID: "cluster-d"}},
		Routes: []RouteObservation{
			{ID: "route-1", DefinitionID: "definition-1"},
			{ID: "route-2", DefinitionID: "definition-2"},
			{ID: "route-3", DefinitionID: "definition-3"},
			{ID: "route-4", DefinitionID: "definition-4"},
			{ID: "route-5", DefinitionID: "definition-1"},
			{ID: "route-6", DefinitionID: "definition-2"},
			{ID: "route-7", DefinitionID: "definition-3"},
			{ID: "route-8", DefinitionID: "definition-4"},
			{ID: "route-e2e-release", DefinitionID: "e2e-release-target"},
			{ID: "route-e2e-isolation", DefinitionID: "e2e-isolation-target"},
			{ID: "route-e2e-emergency", DefinitionID: "e2e-emergency-target"},
			{ID: "route-e2e-restart", DefinitionID: "e2e-restart-target"},
		},
		Definitions: []DefinitionObservation{
			{ID: "definition-1"},
			{ID: "definition-2"},
			{ID: "definition-3"},
			{ID: "definition-4"},
			{ID: "e2e-release-target"},
			{ID: "e2e-isolation-target"},
			{ID: "e2e-emergency-target"},
			{ID: "e2e-restart-target"},
		},
		Bundles: []BundleObservation{{ID: "bundle-1"}},
	}
}

func completeExpectedIdentity() ExpectedIdentity {
	return ExpectedIdentity{
		Customers:        2,
		Clusters:         4,
		RoutesBasic:      8,
		DefinitionsBasic: 4,
		Bundles:          1,
		E2EDefinitionIDs: []string{"e2e-release-target", "e2e-isolation-target", "e2e-emergency-target", "e2e-restart-target"},
	}
}

func TestInventoryStageRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		fake inventoryFake
		want error
	}{
		{name: "pass", fake: inventoryFake{observation: completeInventory()}},
		{
			name: "identity mismatch",
			fake: inventoryFake{observation: func() InventoryObservation {
				observation := completeInventory()
				observation.Customers = observation.Customers[:1]
				return observation
			}()},
			want: ErrFixtureStale,
		},
		{name: "observation error", fake: inventoryFake{err: errors.New("database secret")}, want: ErrSnapshotNotFound},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			stage := NewInventoryStage(test.fake, completeExpectedIdentity())
			err := stage.Run(context.Background(), nil)
			if test.want == nil {
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("Run() error = %v, want errors.Is(..., %v)", err, test.want)
			}
			if err.Error() == "database secret" {
				t.Fatal("Run() leaked reader error")
			}
		})
	}
}

func TestInventoryStageDifferencesAreStable(t *testing.T) {
	t.Parallel()

	observation := completeInventory()
	observation.Definitions = observation.Definitions[:4]
	stage := NewInventoryStage(inventoryFake{observation: observation}, completeExpectedIdentity())
	first := stage.Run(context.Background(), nil)
	second := stage.Run(context.Background(), nil)
	if first == nil || second == nil || first.Error() != second.Error() {
		t.Fatalf("Run() errors are not stable: first=%v second=%v", first, second)
	}
	if len(stage.Differences()) == 0 {
		t.Fatal("Differences() is empty")
	}
}
