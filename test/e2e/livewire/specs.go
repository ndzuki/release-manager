package livewire

// This file assembles the canonical stage graph that cmd/e2e runs. It is the
// single place where a stage is bound to its live dependencies, so the CLI
// never has to know about Connect clients, kubeconfigs, or compensation
// registries, and a stage can never be wired with a dependency that belongs to
// another stage.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	e2e "github.com/ndzuki/release-manager/test/e2e"
	"github.com/ndzuki/release-manager/test/e2e/stages"
)

// DefaultEmergencyReplicas is the replica count the emergency stage drives
// before its compensation restores the observed baseline.
//
// It is two, and it is a constant rather than a config value, for three
// reasons that pin each other:
//
//   - the seed fixture gives only e2e-emergency-target a
//     max_emergency_replicas ceiling (4) and it is required to stay >= 2, so a
//     request for 2 is inside the documented policy window for this definition
//     and inside the headroom the fixture deliberately keeps;
//   - the prerequisite smoke gate proves exactly this change end to end
//     (set_replicas=2 -> SUCCEEDED/APPLIED -> restore), so the stage exercises
//     the same effect the gate already validates rather than a second,
//     unvalidated one;
//   - the stage itself refuses to run when the observed baseline equals the
//     target ("the change would not be observable"), so if a future fixture
//     seeds a baseline of 2 the run fails closed with fixture_stale instead of
//     silently asserting nothing.
//
// A config-derived count would need a new env-config field; none exists, and
// inventing one here would move the decision away from the single place that
// validates the emergency policy. This constant is that place.
const DefaultEmergencyReplicas int32 = 2

// Canonical E2E definition names. They are the fixture's stable logical keys
// (dev-fixture.json `definitions` keys), which is what the env-config publishes
// in seed.expected_identity.e2e_definition_ids.
const (
	releaseDefinitionName   = "e2e-release-target"
	isolationDefinitionName = "e2e-isolation-target"
	emergencyDefinitionName = "e2e-emergency-target"
	restartDefinitionName   = "e2e-restart-target"
)

// targetSlots is the canonical order the env-config assembler emits
// seed.e2e_upgrade_targets in (Makefile target `e2e-env-config`): release,
// isolation, restart.
var targetSlots = []string{releaseDefinitionName, isolationDefinitionName, restartDefinitionName}

// Specs builds the canonical stage graph over the live environment declared by
// cfg.
//
// The returned specs are always complete: every name in e2e.CanonicalStages is
// bound to a real implementation with the dependency edges from
// e2e.CanonicalDependencies, so the graph cannot drift from the canonical
// vocabulary. Any unusable input — an unparsable endpoint, a missing
// kubeconfig, a seed that does not declare the E2E definitions — returns an
// error and no specs at all, so a caller can never end up running a partially
// wired graph.
//
// Note on identifier shapes: the specs carry the seed values verbatim. The
// env-config publishes a mix of the fixture's stable logical keys
// (e2e_definition_ids) and server-side definition ids (e2e_upgrade_targets),
// and this function deliberately does not translate between them — an
// invented translation would hide a contract gap that the affected stage
// reports itself with a stable code.
func Specs(cfg *e2e.Config) ([]e2e.StageSpec, error) {
	if cfg == nil {
		return nil, errors.New("livewire: nil config")
	}
	assembled, err := newGraph(cfg)
	if err != nil {
		return nil, err
	}
	return assembled.specs()
}

// graph holds the run-scoped stage implementations. Exactly one instance of
// each is created per run, and exactly one compensation registry is shared by
// the stages that can compensate.
type graph struct {
	controlPlane *stages.ControlPlaneStage
	inventory    *stages.InventoryStage
	artifact     *stages.ArtifactStage
	release      *stages.ReleaseStage
	isolation    *releaseInvariantBinding
	emergency    *stages.EmergencyStage
	restart      *stages.RestartStage
}

// implementations maps each canonical stage name onto its implementation.
func (g *graph) implementations() map[string]e2e.Stage {
	return map[string]e2e.Stage{
		e2e.StageControlPlane: g.controlPlane,
		e2e.StageInventory:    g.inventory,
		e2e.StageArtifactName: g.artifact,
		e2e.StageRelease:      g.release,
		e2e.StageIsolation:    g.isolation,
		e2e.StageEmergency:    g.emergency,
		e2e.StageRestart:      g.restart,
	}
}

// specs renders the graph as ordered StageSpecs. The order and the dependency
// edges come from the canonical vocabulary, so adding a canonical stage without
// an implementation fails closed here rather than producing a graph whose
// missing stage is silently ignored.
func (g *graph) specs() ([]e2e.StageSpec, error) {
	return specsFromImplementations(g.implementations())
}

// specsFromImplementations renders the canonical graph over one implementation
// map. It is the single definition of the graph shape: every canonical name
// must resolve to a live stage, and every dependency edge is copied from
// e2e.CanonicalDependencies.
func specsFromImplementations(implementations map[string]e2e.Stage) ([]e2e.StageSpec, error) {
	out := make([]e2e.StageSpec, 0, len(e2e.CanonicalStages))
	for _, name := range e2e.CanonicalStages {
		implementation, ok := implementations[name]
		if !ok || isNilStage(implementation) {
			return nil, fmt.Errorf("livewire: canonical stage %q has no implementation", name)
		}
		out = append(out, e2e.StageSpec{
			Name:         name,
			Stage:        implementation,
			Dependencies: append([]string(nil), e2e.CanonicalDependencies[name]...),
		})
	}
	return out, nil
}

// isNilStage reports whether a stage is absent, including the typed-nil case: a
// nil *stages.ControlPlaneStage stored in an e2e.Stage interface compares
// unequal to nil, and a stage that only fails when it runs is not a fail-closed
// graph.
func isNilStage(stage e2e.Stage) bool {
	if stage == nil {
		return true
	}
	value := reflect.ValueOf(stage)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// newGraph builds every live dependency and the stages over them. Each failure
// aborts the whole graph: a stage whose client, clientset, or seed binding is
// unavailable must never be replaced by a no-op.
func newGraph(cfg *e2e.Config) (*graph, error) {
	clients, err := e2e.NewClientBundle(cfg)
	if err != nil {
		return nil, fmt.Errorf("livewire: build connect clients: %w", err)
	}
	connector, err := NewWithClients(cfg, clients)
	if err != nil {
		return nil, fmt.Errorf("livewire: build connector: %w", err)
	}
	observer, err := NewControlPlaneObserver(cfg, connector)
	if err != nil {
		return nil, fmt.Errorf("livewire: build control-plane observer: %w", err)
	}
	reader, err := NewFormalReader(connector)
	if err != nil {
		return nil, fmt.Errorf("livewire: build read-only reader: %w", err)
	}
	waiter, err := NewControlPlaneWaiter(cfg, clients, connector.Session())
	if err != nil {
		return nil, fmt.Errorf("livewire: build control-plane waiter: %w", err)
	}
	kubernetesClient, err := NewKubernetesClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("livewire: build kubernetes client: %w", err)
	}
	replicas, err := NewReplicaObserver(kubernetesClient)
	if err != nil {
		return nil, fmt.Errorf("livewire: build replica observer: %w", err)
	}
	restartProbe, err := connector.NewRestartProbe(kubernetesClient)
	if err != nil {
		return nil, fmt.Errorf("livewire: build restart probe: %w", err)
	}
	releaseTarget, isolationTarget, err := seedUpgradeTargets(cfg)
	if err != nil {
		return nil, err
	}
	if err := stages.ValidateIsolationTargets(releaseTarget, isolationTarget); err != nil {
		return nil, fmt.Errorf("livewire: %w", err)
	}
	emergencyDefinitionID, err := seedEmergencyDefinitionID(cfg)
	if err != nil {
		return nil, err
	}

	registry := e2e.NewCompensationRegistry()
	controlPlane := stages.NewControlPlaneStage(observer)
	artifact, err := newArtifactStage(reader, releaseTarget)
	if err != nil {
		return nil, err
	}
	release := stages.NewReleaseStage(connector, connector, registry, releaseTarget)
	isolation := stages.NewIsolationStage(connector, connector, registry, isolationTarget)
	emergency := stages.NewEmergencyStage(connector, replicas, registry,
		stages.WriteTarget{Name: emergencyDefinitionID, DefinitionID: emergencyDefinitionID},
		DefaultEmergencyReplicas)

	// The restart barrier is guarded by a control-plane check. D-026 D8 asks
	// for the guard when the control-plane stage is not part of the same run,
	// and the graph builder does not know the run's selection; installing it
	// unconditionally costs one extra read-only observation and can only
	// strengthen the barrier, because restarting an already broken control
	// plane is exactly what the guard exists to prevent. It is a separate stage
	// instance over the same observer so the graph node and the guard never
	// share mutable state.
	guard := stages.NewControlPlaneStage(observer)
	restart := stages.NewRestartStage(restartProbe, waiter).WithControlPlaneGuard(
		func(ctx context.Context) error { return guard.Run(ctx, nil) },
	)

	return &graph{
		controlPlane: controlPlane,
		inventory:    stages.NewInventoryStage(reader, cfg.Seed.ExpectedIdentity),
		artifact:     artifact,
		release:      release,
		isolation:    &releaseInvariantBinding{IsolationStage: isolation, release: release, definition: releaseTarget.DefinitionID},
		emergency:    emergency,
		restart:      restart,
	}, nil
}

// newArtifactStage binds the artifact stage to the release target: the seed
// publishes the bundle id per definition, and the artifact stage must observe
// that exact bundle as validated for the release definition it upgrades.
//
// Digest and Route are left unset because the env-config schema publishes
// neither. Both are optional expectations in stages.ArtifactExpectation
// (validateBundle and validateRoute only compare a field that is set), so
// nothing the config could have provided is skipped.
func newArtifactStage(reader stages.ArtifactReader, releaseTarget stages.WriteTarget) (*stages.ArtifactStage, error) {
	if strings.TrimSpace(releaseTarget.BundleID) == "" {
		return nil, fmt.Errorf("livewire: seed.e2e_upgrade_targets.%s.bundle_id is missing", releaseDefinitionName)
	}
	return stages.NewArtifactStage(reader, stages.ArtifactExpectation{
		BundleID:            releaseTarget.BundleID,
		ReleaseDefinitionID: releaseTarget.DefinitionID,
	}), nil
}

// seedUpgradeTargets resolves the release and isolation upgrade targets.
//
// The env-config assembler (`make e2e-env-config`) emits seed.e2e_upgrade_targets
// as a fixed-order array literal — release, isolation, restart — each carrying
// the server-side definition id read from dev-fixture.json, because the public
// API exposes no mapping from the fixture's stable logical key to the
// server-generated definition id. A config that instead carries the logical key
// in definition_id (the configs/e2e.dev.yaml template shape, which
// config.validateUpgradeTargets currently requires) resolves by name.
//
// The two selectors are cross-checked: when logical names are present, the
// positional order must agree with them, so a reordered assembler fails closed
// instead of silently aiming the release upgrade at the isolation definition.
// When only opaque ids are present that cross-check is impossible, and the
// assembler's array order is the only correlation available.
//
// The restart target is deliberately not returned: the restart stage's input is
// the K3d restart binding (namespaces and Deployment names), never an upgrade
// target, so nothing consumes the third slot.
func seedUpgradeTargets(cfg *e2e.Config) (release, isolation stages.WriteTarget, err error) {
	targets := cfg.Seed.E2EUpgradeTargets
	byName := make(map[string]e2e.E2EUpgradeTarget, len(targets))
	for _, target := range targets {
		if _, ok := byName[target.DefinitionID]; !ok {
			byName[target.DefinitionID] = target
		}
	}
	named := make([]e2e.E2EUpgradeTarget, 0, len(targetSlots))
	matched := 0
	for _, slot := range targetSlots {
		target, ok := byName[slot]
		if ok {
			matched++
		}
		named = append(named, target)
	}
	switch {
	case matched == len(targetSlots):
		return writeTargetOf(targetSlots[0], named[0]), writeTargetOf(targetSlots[1], named[1]), nil
	case matched != 0:
		return stages.WriteTarget{}, stages.WriteTarget{}, fmt.Errorf(
			"livewire: seed.e2e_upgrade_targets names %d of the %d canonical targets; it must name all of them or none",
			matched, len(targetSlots))
	case len(targets) < len(targetSlots):
		return stages.WriteTarget{}, stages.WriteTarget{}, fmt.Errorf(
			"livewire: seed.e2e_upgrade_targets has %d entries; the %d canonical targets must all be declared",
			len(targets), len(targetSlots))
	}
	return writeTargetOf(targetSlots[0], targets[0]), writeTargetOf(targetSlots[1], targets[1]), nil
}

// writeTargetOf binds one seed entry to its canonical stage-facing name.
func writeTargetOf(name string, target e2e.E2EUpgradeTarget) stages.WriteTarget {
	return stages.WriteTarget{
		Name:             name,
		DefinitionID:     target.DefinitionID,
		BundleID:         target.BundleID,
		ValuesRevisionID: target.ValuesRevisionID,
	}
}

// seedEmergencyDefinitionID resolves the emergency definition from the seed's
// declared E2E definition ids.
//
// The env-config publishes the fixture's stable logical keys in
// seed.expected_identity.e2e_definition_ids, so the declared key is used
// verbatim as the definition identifier — the same value expected_identity
// already matches observed definitions against. This inherits the contract gap
// described on Specs: the live orchestrator assigns its own definition ids
// (internal/orchestrator/definition.go mints a UUID and keeps the release name
// in ReleaseDefinition.Name), so a live run can only match this value once the
// fixture and the API agree on publishing the server-side id for the logical
// key. The lookup itself fails closed rather than guessing: a seed that does not
// declare the emergency definition returns a build error instead of letting the
// emergency stage target an unverified definition.
func seedEmergencyDefinitionID(cfg *e2e.Config) (string, error) {
	for _, id := range cfg.Seed.ExpectedIdentity.E2EDefinitionIDs {
		if strings.TrimSpace(id) == emergencyDefinitionName {
			return emergencyDefinitionName, nil
		}
	}
	return "", fmt.Errorf("livewire: seed.expected_identity.e2e_definition_ids must declare %s", emergencyDefinitionName)
}

// releaseInvariantBinding defers binding the isolation stage's release
// invariant to the moment the isolation stage runs.
//
// The invariant is the release target's post-upgrade revision, which only
// exists after the release stage has succeeded; wiring it when the graph is
// built would pin a revision of zero, which stages.IsolationStage correctly
// rejects as fixture_stale. The runner executes isolation after its
// prerequisites, so the read below observes a completed release.
//
// When the release stage did not run in this run (selection is explicit and
// never pulls prerequisites in), no post-upgrade revision exists and the
// isolation stage falls back to its documented distinct-definition check
// instead of asserting an invariant it cannot know.
type releaseInvariantBinding struct {
	*stages.IsolationStage
	release    *stages.ReleaseStage
	definition string
}

var _ e2e.Stage = (*releaseInvariantBinding)(nil)

// Run implements e2e.Stage. Name is inherited from the embedded isolation
// stage, so the spec keeps its canonical name without a second source of truth.
func (b *releaseInvariantBinding) Run(ctx context.Context, fixture *e2e.Fixture) error {
	if b == nil || b.IsolationStage == nil {
		return errors.New("livewire: isolation stage is unavailable")
	}
	if b.release != nil {
		if revision := b.release.UpgradedRevision(); revision > 0 {
			b.WithReleaseInvariant(b.definition, revision)
		}
	}
	return b.IsolationStage.Run(ctx, fixture)
}
