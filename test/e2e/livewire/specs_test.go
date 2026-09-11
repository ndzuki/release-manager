package livewire

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e2e "github.com/ndzuki/release-manager/test/e2e"
	"github.com/ndzuki/release-manager/test/e2e/stages"
)

func TestSpecsBuildsTheCanonicalGraph(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	specs, err := Specs(h.cfg)
	if err != nil {
		t.Fatalf("Specs() error = %v", err)
	}

	if len(specs) != len(e2e.CanonicalStages) {
		t.Fatalf("Specs() returned %d stages, want %d", len(specs), len(e2e.CanonicalStages))
	}
	for index, spec := range specs {
		want := e2e.CanonicalStages[index]
		if spec.Name != want {
			t.Fatalf("spec[%d].Name = %q, want %q", index, spec.Name, want)
		}
		if spec.Stage == nil {
			t.Fatalf("stage %s has no implementation", spec.Name)
		}
		if spec.Stage.Name() != want {
			t.Fatalf("stage %s reports Name() = %q", spec.Name, spec.Stage.Name())
		}
		wantDeps := e2e.CanonicalDependencies[want]
		if strings.Join(spec.Dependencies, ",") != strings.Join(wantDeps, ",") {
			t.Fatalf("stage %s dependencies = %v, want %v", spec.Name, spec.Dependencies, wantDeps)
		}
	}
}

func TestSpecsBindsRealStageImplementations(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	specs, err := Specs(h.cfg)
	if err != nil {
		t.Fatalf("Specs() error = %v", err)
	}
	byName := map[string]e2e.StageSpec{}
	for _, spec := range specs {
		byName[spec.Name] = spec
	}

	// Every canonical stage must be a concrete live implementation. A
	// fail-closed placeholder (e2e.UnimplementedSpec) is a *e2e.StageFunc and
	// fails every assertion below, so this also proves the graph carries no
	// not-implemented stand-in.
	want := map[string]any{
		e2e.StageControlPlane: &stages.ControlPlaneStage{},
		e2e.StageInventory:    &stages.InventoryStage{},
		e2e.StageArtifactName: &stages.ArtifactStage{},
		e2e.StageRelease:      &stages.ReleaseStage{},
		e2e.StageIsolation:    &releaseInvariantBinding{},
		e2e.StageEmergency:    &stages.EmergencyStage{},
		e2e.StageRestart:      &stages.RestartStage{},
	}
	for name, pointer := range want {
		got := reflect.TypeOf(byName[name].Stage)
		if got != reflect.TypeOf(pointer) {
			t.Fatalf("stage %s = %v, want %v", name, got, reflect.TypeOf(pointer))
		}
	}
}

func TestSpecsSharesOneCompensationRegistry(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	specs, err := Specs(h.cfg)
	if err != nil {
		t.Fatalf("Specs() error = %v", err)
	}
	byName := map[string]e2e.StageSpec{}
	for _, spec := range specs {
		byName[spec.Name] = spec
	}

	// The release and isolation stages compensate through the same registry:
	// two registries would make the LIFO unwind order undefined, and a rollback
	// would leave the other stage's upgrade applied.
	release := registryPointer(t, byName[e2e.StageRelease].Stage)
	isolation := registryPointer(t, byName[e2e.StageIsolation].Stage)
	if release == 0 || isolation == 0 {
		t.Fatal("release and isolation stages must both hold a compensation registry")
	}
	if release != isolation {
		t.Fatal("release and isolation stages hold different compensation registries")
	}
}

// registryPointer reads the unexported registry field of a stage instance. The
// field lives in another package, so the pointer is read through reflection; a
// rename makes the assertion fail loudly rather than silently pass.
func registryPointer(t *testing.T, stage e2e.Stage) uintptr {
	t.Helper()

	value := reflect.ValueOf(stage)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		t.Fatalf("stage %T is not a non-nil pointer", stage)
	}
	field := value.Elem().FieldByName("registry")
	if !field.IsValid() || field.Kind() != reflect.Pointer {
		t.Fatalf("stage %T has no registry pointer field", stage)
	}
	return field.Pointer()
}

func TestSpecsFailsClosedOnUnusableConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(t *testing.T, cfg *e2e.Config)
	}{
		{
			name: "missing kubeconfig file",
			mutate: func(t *testing.T, cfg *e2e.Config) {
				cfg.K3d.Kubeconfig = filepath.Join(t.TempDir(), "missing-kubeconfig.yaml")
			},
		},
		{
			name: "empty kubeconfig path",
			mutate: func(_ *testing.T, cfg *e2e.Config) {
				cfg.K3d.Kubeconfig = ""
			},
		},
		{
			name: "empty endpoint",
			mutate: func(_ *testing.T, cfg *e2e.Config) {
				cfg.Endpoints.ReleaseOperator = ""
			},
		},
		{
			name: "missing emergency definition",
			mutate: func(_ *testing.T, cfg *e2e.Config) {
				cfg.Seed.ExpectedIdentity.E2EDefinitionIDs = []string{"e2e-release-target"}
			},
		},
		{
			name: "missing upgrade targets",
			mutate: func(_ *testing.T, cfg *e2e.Config) {
				cfg.Seed.E2EUpgradeTargets = nil
			},
		},
		{
			name: "one named target only",
			mutate: func(_ *testing.T, cfg *e2e.Config) {
				cfg.Seed.E2EUpgradeTargets = cfg.Seed.E2EUpgradeTargets[:1]
			},
		},
		{
			name: "aliased isolation target",
			mutate: func(_ *testing.T, cfg *e2e.Config) {
				targets := append([]e2e.E2EUpgradeTarget(nil), cfg.Seed.E2EUpgradeTargets...)
				targets[1].DefinitionID = targets[0].DefinitionID
				cfg.Seed.E2EUpgradeTargets = targets
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			test.mutate(t, h.cfg)
			specs, err := Specs(h.cfg)
			if err == nil {
				t.Fatalf("Specs() error = nil, want a fail-closed error (got %d specs)", len(specs))
			}
			if specs != nil {
				t.Fatalf("Specs() returned %d specs alongside an error, want none", len(specs))
			}
		})
	}
}

func TestSpecsRejectsNilConfig(t *testing.T) {
	t.Parallel()

	specs, err := Specs(nil)
	if err == nil {
		t.Fatal("Specs(nil) error = nil, want a fail-closed error")
	}
	if specs != nil {
		t.Fatalf("Specs(nil) returned %d specs, want none", len(specs))
	}
}

func TestSpecsAcceptsAssemblerOrderedServerIDs(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	// The env-config assembler publishes server-generated definition ids in the
	// fixed order release, isolation, restart. validateUpgradeTargets currently
	// only accepts the logical names, so the assembler shape is applied to the
	// loaded config here.
	h.cfg.Seed.E2EUpgradeTargets = []e2e.E2EUpgradeTarget{
		{DefinitionID: "11111111-1111-1111-1111-111111111111", BundleID: "bundle-1", ValuesRevisionID: "values-1"},
		{DefinitionID: "22222222-2222-2222-2222-222222222222", BundleID: "bundle-1", ValuesRevisionID: "values-1"},
		{DefinitionID: "33333333-3333-3333-3333-333333333333", BundleID: "bundle-1", ValuesRevisionID: "values-1"},
	}
	specs, err := Specs(h.cfg)
	if err != nil {
		t.Fatalf("Specs() error = %v", err)
	}
	if len(specs) != len(e2e.CanonicalStages) {
		t.Fatalf("Specs() returned %d stages, want %d", len(specs), len(e2e.CanonicalStages))
	}

	release, isolation, err := seedUpgradeTargets(h.cfg)
	if err != nil {
		t.Fatalf("seedUpgradeTargets() error = %v", err)
	}
	if release.Name != releaseDefinitionName || release.DefinitionID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("release target = %+v, want the first assembler slot", release)
	}
	if isolation.Name != isolationDefinitionName || isolation.DefinitionID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("isolation target = %+v, want the second assembler slot", isolation)
	}
}

func TestSeedUpgradeTargetsFailsClosedOnPartialNaming(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	// Two logical names plus one opaque id: part of the set resolves by name, so
	// the positional order cannot be cross-checked and selection fails closed.
	h.cfg.Seed.E2EUpgradeTargets = []e2e.E2EUpgradeTarget{
		{DefinitionID: releaseDefinitionName, BundleID: "bundle-1", ValuesRevisionID: "values-1"},
		{DefinitionID: isolationDefinitionName, BundleID: "bundle-1", ValuesRevisionID: "values-1"},
		{DefinitionID: "33333333-3333-3333-3333-333333333333", BundleID: "bundle-1", ValuesRevisionID: "values-1"},
	}
	if _, _, err := seedUpgradeTargets(h.cfg); err == nil {
		t.Fatal("seedUpgradeTargets() error = nil, want a fail-closed error")
	}
}

func TestSeedUpgradeTargetsRequiresAllSlots(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.cfg.Seed.E2EUpgradeTargets = []e2e.E2EUpgradeTarget{
		{DefinitionID: "11111111-1111-1111-1111-111111111111", BundleID: "bundle-1", ValuesRevisionID: "values-1"},
	}
	if _, _, err := seedUpgradeTargets(h.cfg); err == nil {
		t.Fatal("seedUpgradeTargets() error = nil, want a fail-closed error")
	}
}

func TestSpecsBindsArtifactToTheReleaseTarget(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.cfg.Seed.E2EUpgradeTargets[0].BundleID = "bundle-release"

	specs, err := Specs(h.cfg)
	if err != nil {
		t.Fatalf("Specs() error = %v", err)
	}
	artifact := stageOf[*stages.ArtifactStage](t, specs, e2e.StageArtifactName)
	expectation := reflect.ValueOf(artifact).Elem().FieldByName("expectation")
	if !expectation.IsValid() {
		t.Fatal("artifact stage has no expectation field")
	}
	if got := expectation.FieldByName("BundleID").String(); got != "bundle-release" {
		t.Fatalf("artifact expectation BundleID = %q, want the release target bundle", got)
	}
	if got := expectation.FieldByName("ReleaseDefinitionID").String(); got != releaseDefinitionName {
		t.Fatalf("artifact expectation ReleaseDefinitionID = %q, want the release definition", got)
	}
	// Digest and Route are deliberately unset: the env-config schema publishes
	// neither, and validateBundle/validateRoute only compare a set field.
	if got := expectation.FieldByName("Digest").String(); got != "" {
		t.Fatalf("artifact expectation Digest = %q, want it unset", got)
	}
}

func TestArtifactStageRejectsMissingBundle(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	reader, err := NewFormalReader(h.connector)
	if err != nil {
		t.Fatalf("NewFormalReader() error = %v", err)
	}
	if _, err := newArtifactStage(reader, stages.WriteTarget{DefinitionID: releaseDefinitionName}); err == nil {
		t.Fatal("newArtifactStage() error = nil, want a fail-closed error for a missing bundle id")
	}
}

func TestSpecsWiresTheEmergencyReplicaTarget(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	specs, err := Specs(h.cfg)
	if err != nil {
		t.Fatalf("Specs() error = %v", err)
	}
	emergency := stageOf[*stages.EmergencyStage](t, specs, e2e.StageEmergency)
	replicas := reflect.ValueOf(emergency).Elem().FieldByName("replicas")
	if !replicas.IsValid() {
		t.Fatal("emergency stage has no replicas field")
	}
	if got := int32(replicas.Int()); got != DefaultEmergencyReplicas {
		t.Fatalf("emergency replica target = %d, want DefaultEmergencyReplicas (%d)", got, DefaultEmergencyReplicas)
	}
	target := reflect.ValueOf(emergency).Elem().FieldByName("target")
	if got := target.FieldByName("DefinitionID").String(); got != emergencyDefinitionName {
		t.Fatalf("emergency definition = %q, want %s", got, emergencyDefinitionName)
	}
}

func TestReleaseInvariantBindingBindsOnlyAfterRelease(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	specs, err := Specs(h.cfg)
	if err != nil {
		t.Fatalf("Specs() error = %v", err)
	}
	binding := stageOf[*releaseInvariantBinding](t, specs, e2e.StageIsolation)
	if binding.IsolationStage == nil {
		t.Fatal("binding has no isolation stage")
	}
	if binding.release == nil {
		t.Fatal("binding has no release stage to read the invariant from")
	}
	if binding.definition != releaseDefinitionName {
		t.Fatalf("binding definition = %q, want %q", binding.definition, releaseDefinitionName)
	}
	// No release has run, so the invariant is deliberately unbound and the
	// isolation stage keeps its documented distinct-definition fallback.
	if revision := binding.release.UpgradedRevision(); revision != 0 {
		t.Fatalf("UpgradedRevision() = %d before a release, want 0", revision)
	}
}

func TestReleaseInvariantBindingRejectsNilStage(t *testing.T) {
	t.Parallel()

	var binding *releaseInvariantBinding
	if err := binding.Run(context.Background(), nil); err == nil {
		t.Fatal("nil binding Run() error = nil, want a fail-closed error")
	}
	if err := (&releaseInvariantBinding{}).Run(context.Background(), nil); err == nil {
		t.Fatal("binding without an isolation stage Run() error = nil, want a fail-closed error")
	}
}

// stageOf returns the concrete implementation a canonical stage name was bound
// to, and fails the test when the name is missing or bound to another type.
func stageOf[T any](t *testing.T, specs []e2e.StageSpec, name string) T {
	t.Helper()

	var zero T
	for _, spec := range specs {
		if spec.Name != name {
			continue
		}
		implementation, ok := spec.Stage.(T)
		if !ok {
			t.Fatalf("stage %s = %T, want %T", name, spec.Stage, zero)
		}
		return implementation
	}
	t.Fatalf("stage %s is missing from the graph", name)
	return zero
}

func TestSpecsFromImplementationsFailsClosedOnMissingStages(t *testing.T) {
	t.Parallel()

	if _, err := specsFromImplementations(map[string]e2e.Stage{}); err == nil {
		t.Fatal("specsFromImplementations(empty) error = nil, want a fail-closed error")
	}
	// A typed-nil stage is not an implementation either: comparing an interface
	// to nil is not enough to prove a stage can run.
	partial := map[string]e2e.Stage{e2e.StageControlPlane: (*stages.ControlPlaneStage)(nil)}
	if _, err := specsFromImplementations(partial); err == nil {
		t.Fatal("specsFromImplementations(typed nil) error = nil, want a fail-closed error")
	}
	// An unknown canonical name must not make the graph drift: the rendered
	// graph is always exactly the canonical vocabulary.
	h := newHarness(t)
	specs, err := Specs(h.cfg)
	if err != nil {
		t.Fatalf("Specs() error = %v", err)
	}
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	if strings.Join(names, ",") != strings.Join(e2e.CanonicalStages, ",") {
		t.Fatalf("graph names = %v, want the canonical order %v", names, e2e.CanonicalStages)
	}
}

func TestDefaultEmergencyReplicasIsTheSmokeGatedChange(t *testing.T) {
	t.Parallel()

	// REQ-032 §171 bounds set_replicas by the definition's
	// max_emergency_replicas, and the seed fixture gives only
	// e2e-emergency-target a ceiling of 4, so a target of 2 stays inside the
	// policy window the fixture deliberately keeps. The prerequisite smoke gate
	// drives exactly this change.
	if DefaultEmergencyReplicas != 2 {
		t.Fatalf("DefaultEmergencyReplicas = %d, want the smoke-gated value 2", DefaultEmergencyReplicas)
	}
}

func TestIsNilStageDetectsTypedNil(t *testing.T) {
	t.Parallel()

	if isNilStage(nil) != true {
		t.Fatal("isNilStage(nil) = false, want true")
	}
	if isNilStage((*stages.ControlPlaneStage)(nil)) != true {
		t.Fatal("isNilStage(typed nil) = false, want true")
	}
	if isNilStage(&stages.ControlPlaneStage{}) != false {
		t.Fatal("isNilStage(stage) = true, want false")
	}
}
