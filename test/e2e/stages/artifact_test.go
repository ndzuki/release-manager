package stages

import (
	"context"
	"errors"
	"testing"
)

type artifactFake struct {
	bundle    BundleObservation
	inventory InventoryObservation
	bundleErr error
	invErr    error
}

func (f artifactFake) GetBundle(context.Context, BundleRequest) (BundleObservation, error) {
	return f.bundle, f.bundleErr
}

func (f artifactFake) ListReleaseInventory(context.Context) (InventoryObservation, error) {
	return f.inventory, f.invErr
}

func TestArtifactStageRun(t *testing.T) {
	t.Parallel()

	base := artifactFake{
		bundle: BundleObservation{
			ID:     "bundle-1",
			Digest: "sha256:bundle",
			Status: BundleStatusValidated,
		},
		inventory: InventoryObservation{
			Rows: []InventoryRow{{
				CustomerID:   "customer-a",
				ClusterID:    "cluster-a",
				DefinitionID: "definition-1",
				Namespace:    "release-a",
			}},
		},
	}

	tests := []struct {
		name string
		fake artifactFake
		want error
	}{
		{name: "pass", fake: base},
		{name: "bundle lookup failure", fake: func() artifactFake {
			fake := base
			fake.bundleErr = errors.New("private transport detail")
			return fake
		}(), want: ErrSnapshotNotFound},
		{name: "bundle digest drift", fake: func() artifactFake {
			fake := base
			fake.bundle.Digest = "sha256:other"
			return fake
		}(), want: ErrFixtureStale},
		{name: "bundle not validated", fake: func() artifactFake {
			fake := base
			fake.bundle.Status = "received"
			return fake
		}(), want: ErrEnvironmentUnhealthy},
		{name: "inventory lookup failure", fake: func() artifactFake {
			fake := base
			fake.invErr = errors.New("private inventory detail")
			return fake
		}(), want: ErrSnapshotNotFound},
		{name: "route row missing", fake: func() artifactFake {
			fake := base
			fake.inventory.Rows = nil
			return fake
		}(), want: ErrFixtureStale},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			expectation := ArtifactExpectation{
				BundleID:            "bundle-1",
				Digest:              "sha256:bundle",
				ReleaseDefinitionID: "definition-1",
				Route: RouteExpectation{
					CustomerID: "customer-a",
					ClusterID:  "cluster-a",
					Namespace:  "release-a",
				},
			}
			stage := NewArtifactStage(test.fake, expectation)
			err := stage.Run(context.Background(), nil)
			if test.want == nil {
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				refs := stage.Artifacts()
				if len(refs) != 1 || refs[0].ID != "bundle-1" || refs[0].Digest != "sha256:bundle" {
					t.Fatalf("Artifacts() = %#v", refs)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("Run() error = %v, want errors.Is(..., %v)", err, test.want)
			}
		})
	}
}

func TestArtifactStageOptionalRoutes(t *testing.T) {
	t.Parallel()

	stage := NewArtifactStage(artifactFake{
		bundle:    BundleObservation{ID: "bundle-1", Digest: "sha256:bundle", Status: "BUNDLE_STATUS_VALIDATED"},
		inventory: InventoryObservation{Rows: []InventoryRow{{DefinitionID: "definition-1"}}},
	}, ArtifactExpectation{
		BundleID:            "bundle-1",
		Digest:              "sha256:bundle",
		ReleaseDefinitionID: "definition-1",
	})
	if err := stage.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}
