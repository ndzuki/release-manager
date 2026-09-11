package livewire

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ndzuki/release-manager/test/e2e"
	"github.com/ndzuki/release-manager/test/e2e/stages"
)

// BaselineReplicas observes the replica count of every workload the emergency
// stage can change, so cleanup can restore them after a run that dies
// mid-flight (AC-066-23/34).
//
// Only the emergency definition is sampled: it is the sole canonical stage that
// changes replicas. The target list supplies the workload identity, and the
// count itself comes from the read-only typed client, because
// ListEmergencyTargets reports current_replicas as a D7=A unavailable sentinel
// (-1) rather than a live count. Reading that field recorded nothing at all:
// every target was skipped as negative, so the baseline carried no replicas and
// the restore could never run (real smoke 2026-09-11).
//
// A nil config is a programming error; an unreachable environment is not. The
// caller decides, because a baseline read that fails must not by itself abort
// a run whose stages could still succeed.
func BaselineReplicas(ctx context.Context, cfg *e2e.Config) ([]e2e.WorkloadReplicaRef, error) {
	if cfg == nil {
		return nil, errors.New("livewire: nil config")
	}
	clients, err := e2e.NewClientBundle(cfg)
	if err != nil {
		return nil, fmt.Errorf("livewire: build connect clients: %w", err)
	}
	connector, err := NewWithClients(cfg, clients)
	if err != nil {
		return nil, fmt.Errorf("livewire: build connector: %w", err)
	}
	definitionID, err := seedEmergencyDefinitionID(cfg)
	if err != nil {
		return nil, err
	}
	if err := connector.Login(ctx); err != nil {
		return nil, fmt.Errorf("livewire: baseline login: %w", err)
	}
	targets, err := connector.Targets(ctx, definitionID)
	if err != nil {
		return nil, fmt.Errorf("livewire: baseline targets: %w", err)
	}
	// The emergency workload runs in the definition's customer cluster, so the
	// observation is resolved per cluster rather than read from the management
	// plane.
	provider, err := NewClusterContextsClientProvider(cfg)
	if err != nil {
		return nil, fmt.Errorf("livewire: baseline cluster client provider: %w", err)
	}
	observer, err := NewClusterReplicaObserver(provider)
	if err != nil {
		return nil, fmt.Errorf("livewire: baseline replica observer: %w", err)
	}
	return sampleReplicas(ctx, definitionID, targets, observer)
}

// baselineReplicaSampler is the read-only observation the baseline needs; the
// live ReplicaObserver satisfies it.
type baselineReplicaSampler interface {
	ObserveReplicas(ctx context.Context, cluster, namespace, workloadName string) (stages.ReplicaObservation, error)
}

// BaselineRevisions records the current Helm revision of every release the run
// can move, so cleanup can roll a definition back to where the run found it
// (AC-066-28).
//
// The rows come from the same ListReleaseInventory read cleanup itself uses, so
// the baseline and the recovery target are one fact observed at two times rather
// than two projections that can drift. Without this the baseline carried
// revisions nowhere: cleanup reported skipped_revision_restore for every row and
// the rollback half of the recovery contract could never run.
//
// As with the replica sample, an unreachable environment is not a programming
// error; the caller decides, because losing a recovery aid must not abort a run
// whose stages could still succeed.
func BaselineRevisions(ctx context.Context, cfg *e2e.Config) ([]e2e.InventoryRef, error) {
	if cfg == nil {
		return nil, errors.New("livewire: nil config")
	}
	clients, err := e2e.NewClientBundle(cfg)
	if err != nil {
		return nil, fmt.Errorf("livewire: build connect clients: %w", err)
	}
	recovery, err := e2e.NewLiveRecovery(cfg, clients)
	if err != nil {
		return nil, fmt.Errorf("livewire: build baseline recovery reader: %w", err)
	}
	if _, err := recovery.Login(ctx); err != nil {
		return nil, fmt.Errorf("livewire: baseline login: %w", err)
	}
	rows, err := recovery.ListReleaseDigests(ctx)
	if err != nil {
		return nil, fmt.Errorf("livewire: baseline release digests: %w", err)
	}
	return inventoryRefsFromReleaseDigests(rows), nil
}

// inventoryRefsFromReleaseDigests projects the digest read onto the baseline
// shape.
//
// The digest read is the source rather than ListReleaseInventory because cleanup
// compares content, not revision numbers: a rollback advances the revision rather
// than restoring it, so a baseline without a digest cannot say whether a release
// is already back. A row without a definition id or with a non-positive revision
// is dropped -- it names no rollback target, and
// BaselineRecoveryFromSnapshots would discard it again on the way back.
func inventoryRefsFromReleaseDigests(rows []e2e.ReleaseDigest) []e2e.InventoryRef {
	refs := make([]e2e.InventoryRef, 0, len(rows))
	for index := range rows {
		row := rows[index]
		if strings.TrimSpace(row.ReleaseDefinitionID) == "" || row.Revision < 1 {
			continue
		}
		refs = append(refs, e2e.InventoryRef{
			ReleaseDefinitionID: row.ReleaseDefinitionID,
			Revision:            int(row.Revision),
			ValuesDigest:        row.ValuesDigest,
		})
	}
	sort.SliceStable(refs, func(i, j int) bool {
		return refs[i].ReleaseDefinitionID < refs[j].ReleaseDefinitionID
	})
	return refs
}

// sampleReplicas records the observed replica count of each target under the
// authoritative reference a later restore must send.
//
// A workload scaled to zero is a real baseline and is kept: restoring it to zero
// is exactly what cleanup must do. Only an unavailable (negative) count is
// dropped, because a fabricated row would restore a count nobody observed.
func sampleReplicas(ctx context.Context, definitionID string, targets []stages.EmergencyTarget, sampler baselineReplicaSampler) ([]e2e.WorkloadReplicaRef, error) {
	refs := make([]e2e.WorkloadReplicaRef, 0, len(targets))
	for index := range targets {
		target := targets[index]
		reference := target.WorkloadReference()
		if reference == "" || target.Namespace == "" || target.WorkloadName == "" {
			continue
		}
		observation, err := sampler.ObserveReplicas(ctx, target.Cluster, target.Namespace, target.WorkloadName)
		if err != nil {
			return nil, fmt.Errorf("livewire: baseline observe %s: %w", reference, err)
		}
		if observation.Replicas < 0 {
			continue
		}
		refs = append(refs, e2e.WorkloadReplicaRef{
			ReleaseDefinitionID: definitionID,
			Cluster:             target.Cluster,
			WorkloadRef:         reference,
			Replicas:            observation.Replicas,
		})
	}
	sort.SliceStable(refs, func(i, j int) bool {
		return refs[i].WorkloadRef < refs[j].WorkloadRef
	})
	return refs, nil
}
