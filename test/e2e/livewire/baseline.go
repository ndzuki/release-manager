package livewire

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/ndzuki/release-manager/test/e2e"
)

// BaselineReplicas observes the replica count of every workload the emergency
// stage can change, so cleanup can restore them after a run that dies
// mid-flight (AC-066-23/34).
//
// Only the emergency definition is sampled: it is the sole canonical stage that
// changes replicas, and the emergency API is the only interface that reports a
// workload's current count as part of the target identity. Sampling through the
// formal API (rather than deriving names from the chart) keeps the recorded
// reference exactly the one a later restore must use.
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
	refs := make([]e2e.WorkloadReplicaRef, 0, len(targets))
	for index := range targets {
		target := targets[index]
		reference := target.WorkloadReference()
		if reference == "" || target.CurrentReplicas < 0 {
			continue
		}
		refs = append(refs, e2e.WorkloadReplicaRef{
			ReleaseDefinitionID: definitionID,
			WorkloadRef:         reference,
			Replicas:            target.CurrentReplicas,
		})
	}
	sort.SliceStable(refs, func(i, j int) bool {
		return refs[i].WorkloadRef < refs[j].WorkloadRef
	})
	return refs, nil
}
