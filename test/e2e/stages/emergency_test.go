package stages

import (
	"context"
	"errors"
	"testing"

	e2e "github.com/ndzuki/release-manager/test/e2e"
)

// replicaState is the simulated cluster replica count shared by the emergency
// writer and the read-only observer, so the test exercises the real
// "operation terminal != effect applied" ordering.
type replicaState struct{ replicas int32 }

type emergencyFake struct {
	state      *replicaState
	targets    []EmergencyTarget
	targetsErr error

	active    ActiveOperation
	hasActive bool
	activeErr error

	cancels   []string
	cancelErr error

	setRef OperationRef
	setErr error
	sets   []EmergencySetReplicasRequest

	pending  *int32
	await    map[string]OperationRef
	awaitErr map[string]error

	// readyDelta makes the observer report readyReplicas != replicas.
	readyDelta int32
	observeErr error
}

func newEmergencyFake(replicas int32) *emergencyFake {
	return &emergencyFake{
		state: &replicaState{replicas: replicas},
		targets: []EmergencyTarget{{
			WorkloadKind:     "Deployment",
			Namespace:        "release-fixture",
			WorkloadName:     "release-fixture",
			CurrentReplicas:  replicas,
			OperationVersion: "v3",
		}},
		setRef:   OperationRef{ID: "op-emergency", DefinitionID: "def-emergency", Type: "EMERGENCY"},
		await:    map[string]OperationRef{},
		awaitErr: map[string]error{},
	}
}

func (f *emergencyFake) ActiveOperation(context.Context, string) (ActiveOperation, bool, error) {
	return f.active, f.hasActive, f.activeErr
}

func (f *emergencyFake) Targets(context.Context, string) ([]EmergencyTarget, error) {
	return f.targets, f.targetsErr
}

func (f *emergencyFake) SetReplicas(_ context.Context, req EmergencySetReplicasRequest) (OperationRef, error) {
	f.sets = append(f.sets, req)
	if f.setErr != nil {
		return OperationRef{}, f.setErr
	}
	value := req.Replicas
	f.pending = &value
	return f.setRef, nil
}

func (f *emergencyFake) Cancel(_ context.Context, operationID string) error {
	f.cancels = append(f.cancels, operationID)
	return f.cancelErr
}

func (f *emergencyFake) AwaitOperation(_ context.Context, operationID string) (OperationRef, error) {
	if err, ok := f.awaitErr[operationID]; ok {
		return OperationRef{ID: operationID}, err
	}
	if f.pending != nil {
		f.state.replicas = *f.pending
		f.pending = nil
	}
	if ref, ok := f.await[operationID]; ok {
		return ref, nil
	}
	return OperationRef{ID: operationID, Status: wireSucceeded}, nil
}

func (f *emergencyFake) ObserveReplicas(context.Context, string, string) (ReplicaObservation, error) {
	if f.observeErr != nil {
		return ReplicaObservation{}, f.observeErr
	}
	return ReplicaObservation{
		Replicas: f.state.replicas,
		Ready:    f.state.replicas - f.readyDelta,
	}, nil
}

func emergencyTarget() WriteTarget {
	return WriteTarget{Name: "e2e-emergency-target", DefinitionID: "def-emergency"}
}

func TestEmergencyStageRun(t *testing.T) {
	t.Parallel()

	base := func() *emergencyFake {
		fake := newEmergencyFake(1)
		fake.await["op-emergency"] = OperationRef{ID: "op-emergency", Status: wireSucceeded}
		return fake
	}

	tests := []struct {
		name    string
		mutate  func(*emergencyFake)
		want    error
		wantReg int
	}{
		{name: "pass", wantReg: 1},
		{
			name:   "targets unavailable",
			mutate: func(f *emergencyFake) { f.targetsErr = errors.New("private detail") },
			want:   ErrSnapshotNotFound,
		},
		{
			name:   "no targets",
			mutate: func(f *emergencyFake) { f.targets = nil },
			want:   ErrSnapshotNotFound,
		},
		{
			name: "ambiguous targets",
			mutate: func(f *emergencyFake) {
				f.targets = append(f.targets, EmergencyTarget{WorkloadKind: "Deployment", Namespace: "ns", WorkloadName: "other", CurrentReplicas: 1})
			},
			want: ErrEffectUnknown,
		},
		{
			name:   "baseline not positive",
			mutate: func(f *emergencyFake) { f.targets[0].CurrentReplicas = 0 },
			want:   ErrSnapshotNotFound,
		},
		{
			name: "target equals baseline",
			mutate: func(f *emergencyFake) {
				f.targets[0].CurrentReplicas = 2
			},
			want: ErrFixtureStale,
		},
		{
			name:   "set replicas rejected",
			mutate: func(f *emergencyFake) { f.setErr = errors.New("private detail") },
			want:   ErrOperationRejected,
		},
		{
			name: "operation failed terminally",
			mutate: func(f *emergencyFake) {
				f.await["op-emergency"] = OperationRef{ID: "op-emergency", Status: wireFailed}
			},
			want: ErrOperationFailed,
		},
		{
			name:   "effect not applied",
			mutate: func(f *emergencyFake) { f.readyDelta = 1 },
			want:   ErrEffectUnknown,
		},
		{
			name:   "effect unobservable",
			mutate: func(f *emergencyFake) { f.observeErr = errors.New("private detail") },
			want:   ErrEffectUnknown,
		},
		{
			name: "foreign active operation",
			mutate: func(f *emergencyFake) {
				f.hasActive = true
				f.active = ActiveOperation{ID: "op-other", Actor: "user-1", Status: wireQueued}
			},
			want: ErrReleaseBusy,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fake := base()
			if test.mutate != nil {
				test.mutate(fake)
			}
			registry := e2e.NewCompensationRegistry()
			stage := NewEmergencyStage(fake, fake, registry, emergencyTarget(), 2)

			err := stage.Run(context.Background(), nil)
			if test.want == nil {
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				if !stage.Result().Succeeded() {
					t.Fatalf("Result() = %#v, want succeeded", stage.Result())
				}
				if stage.BaselineReplicas() != 1 {
					t.Fatalf("BaselineReplicas() = %d, want 1", stage.BaselineReplicas())
				}
				if got := fake.sets[0].Convergence; got != EmergencyConvergenceRevertOnNextReconcile {
					t.Fatalf("convergence = %q, want REVERT_ON_NEXT_RECONCILE", got)
				}
				if got := fake.sets[0].WorkloadKey; got != "deployment/release-fixture/release-fixture" {
					t.Fatalf("workload key = %q", got)
				}
				if got := fake.sets[0].OperationVersion; got != "v3" {
					t.Fatalf("operation version = %q, want the observed v3", got)
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("Run() error = %v, want errors.Is(..., %v)", err, test.want)
			}
			if registry.Len() != test.wantReg {
				t.Fatalf("registry.Len() = %d, want %d", registry.Len(), test.wantReg)
			}
		})
	}
}

func TestEmergencyStageRejectsPromotionConvergence(t *testing.T) {
	t.Parallel()

	fake := newEmergencyFake(1)
	fake.await["op-emergency"] = OperationRef{
		ID:          "op-emergency",
		Status:      wireSucceeded,
		Convergence: EmergencyConvergenceRequirePromotion,
	}
	registry := e2e.NewCompensationRegistry()
	stage := NewEmergencyStage(fake, fake, registry, emergencyTarget(), 2)

	err := stage.Run(context.Background(), nil)
	if !errors.Is(err, ErrEffectUnknown) {
		t.Fatalf("Run() error = %v, want ErrEffectUnknown for a promotion-backed change", err)
	}
	if registry.Len() != 0 {
		t.Fatalf("registry.Len() = %d, want no compensation after a rejected convergence policy", registry.Len())
	}
}

func TestEmergencyStageRestoreCompensation(t *testing.T) {
	t.Parallel()

	// A non-unit baseline guards against a compensation that hardcodes 1.
	fake := newEmergencyFake(5)
	fake.await["op-emergency"] = OperationRef{ID: "op-emergency", Status: wireSucceeded}
	registry := e2e.NewCompensationRegistry()
	stage := NewEmergencyStage(fake, fake, registry, emergencyTarget(), 2)

	if err := stage.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if fake.state.replicas != 2 {
		t.Fatalf("replicas after change = %d, want 2", fake.state.replicas)
	}
	if err := registry.Run(context.Background()); err != nil {
		t.Fatalf("registry.Run() error = %v", err)
	}
	if len(fake.sets) != 2 {
		t.Fatalf("set_replicas calls = %d, want change + restore", len(fake.sets))
	}
	if fake.sets[1].Replicas != 5 {
		t.Fatalf("restore replicas = %d, want the observed baseline 5", fake.sets[1].Replicas)
	}
	if fake.state.replicas != 5 {
		t.Fatalf("replicas after restore = %d, want 5", fake.state.replicas)
	}
}

func TestEmergencyStageRestoreFailureIsDirty(t *testing.T) {
	t.Parallel()

	fake := newEmergencyFake(1)
	fake.await["op-emergency"] = OperationRef{ID: "op-emergency", Status: wireSucceeded}
	registry := e2e.NewCompensationRegistry()
	stage := NewEmergencyStage(fake, fake, registry, emergencyTarget(), 2)
	if err := stage.Run(context.Background(), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// The restore effect never lands: the operation reports success but the
	// observer keeps reporting ready replicas below the requested count.
	fake.readyDelta = 1
	if err := registry.Run(context.Background()); !errors.Is(err, e2e.ErrCompensationDirty) {
		t.Fatalf("registry.Run() error = %v, want ErrCompensationDirty", err)
	}
}

func TestEmergencyStageMissingDefinition(t *testing.T) {
	t.Parallel()

	registry := e2e.NewCompensationRegistry()
	stage := NewEmergencyStage(newEmergencyFake(1), newEmergencyFake(1), registry, WriteTarget{Name: "e2e-emergency-target"}, 2)
	err := stage.Run(context.Background(), nil)
	if !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("Run() error = %v, want ErrSnapshotNotFound", err)
	}
}

func TestEmergencyTargetWorkloadKey(t *testing.T) {
	t.Parallel()

	target := EmergencyTarget{WorkloadKind: "StatefulSet", Namespace: "ns", WorkloadName: "app"}
	if got := target.WorkloadKey(); got != "statefulset/ns/app" {
		t.Fatalf("WorkloadKey() = %q", got)
	}
}

func TestEmergencyStageInvalidReplicaCount(t *testing.T) {
	t.Parallel()

	registry := e2e.NewCompensationRegistry()
	stage := NewEmergencyStage(newEmergencyFake(1), newEmergencyFake(1), registry, emergencyTarget(), 0)
	if err := stage.Run(context.Background(), nil); !errors.Is(err, ErrSnapshotNotFound) {
		t.Fatalf("Run() error = %v, want ErrSnapshotNotFound for a non-positive target count", err)
	}
}
