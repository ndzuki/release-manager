package livewire

import (
	"context"
	"errors"
	"fmt"
	"sort"

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
	client, err := NewKubernetesClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("livewire: baseline kubernetes client: %w", err)
	}
	observer, err := NewReplicaObserver(client)
	if err != nil {
		return nil, fmt.Errorf("livewire: baseline replica observer: %w", err)
	}
	return sampleReplicas(ctx, definitionID, targets, observer)
}

// baselineReplicaSampler is the read-only observation the baseline needs; the
// live ReplicaObserver satisfies it.
type baselineReplicaSampler interface {
	ObserveReplicas(ctx context.Context, namespace, workloadName string) (stages.ReplicaObservation, error)
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
		observation, err := sampler.ObserveReplicas(ctx, target.Namespace, target.WorkloadName)
		if err != nil {
			return nil, fmt.Errorf("livewire: baseline observe %s: %w", reference, err)
		}
		if observation.Replicas < 0 {
			continue
		}
		refs = append(refs, e2e.WorkloadReplicaRef{
			ReleaseDefinitionID: definitionID,
			WorkloadRef:         reference,
			Replicas:            observation.Replicas,
		})
	}
	sort.SliceStable(refs, func(i, j int) bool {
		return refs[i].WorkloadRef < refs[j].WorkloadRef
	})
	return refs, nil
}
