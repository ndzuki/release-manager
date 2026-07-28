package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndzuki/release-manager/internal/store"
)

func TestCoordinatorRunRequiredFailureStopsLaterStages(t *testing.T) {
	fixture := newCoordinatorFixture(t, []StageDef{
		{Name: StageArtifact, Required: true, Timeout: time.Second},
		{Name: StageRender, Required: true, Timeout: time.Second},
	})
	fixture.outbox.results[StageArtifact] = StageResult{Status: StageFailed, Detail: "digest mismatch"}

	err := fixture.coordinator.Run(t.Context(), fixture.operation)
	require.Error(t, err)
	assert.Equal(t, []StageName{StageArtifact}, fixture.outbox.createdStages())
	assert.Equal(t, store.StatusFailed, fixture.operations.status)
	assert.Equal(t, LifecycleFailed, fixture.lifecycle.overall)
	assert.Equal(t, "artifact", fixture.lifecycle.stages)
}

func TestCoordinatorRunRequiredUnavailableFailsClosed(t *testing.T) {
	fixture := newCoordinatorFixture(t, []StageDef{{Name: StageArtifact, Required: true, Timeout: time.Second}})
	fixture.operators.items = nil

	err := fixture.coordinator.Run(t.Context(), fixture.operation)
	require.Error(t, err)
	assert.Empty(t, fixture.outbox.createdStages())
	assert.Equal(t, store.StatusFailed, fixture.operations.status)
	assert.Equal(t, LifecycleFailed, fixture.lifecycle.overall)
	assert.Equal(t, "artifact", fixture.lifecycle.stages)
}

func TestCoordinatorRunRevokedOnlyOperatorFailsClosed(t *testing.T) {
	fixture := newCoordinatorFixture(t, []StageDef{{Name: StageArtifact, Required: true, Timeout: time.Second}})
	fixture.operators.items = []*store.Operator{{ID: "operator-revoked", Status: store.OperatorRevoked}}

	err := fixture.coordinator.Run(t.Context(), fixture.operation)
	require.Error(t, err)
	assert.Empty(t, fixture.outbox.createdStages())
	assert.Equal(t, store.StatusFailed, fixture.operations.status)
	assert.Equal(t, LifecycleFailed, fixture.lifecycle.overall)
	assert.Equal(t, "artifact", fixture.lifecycle.stages)
}

func TestCoordinatorRunOptionalFailureContinues(t *testing.T) {
	fixture := newCoordinatorFixture(t, []StageDef{
		{Name: StageArtifact, Required: true, Timeout: time.Second},
		{Name: StageRuntimePull, Required: false, Timeout: time.Second},
		{Name: StageRender, Required: true, Timeout: time.Second},
	})
	fixture.outbox.results[StageRuntimePull] = StageResult{Status: StageFailed, Detail: "pull denied"}

	require.NoError(t, fixture.coordinator.Run(t.Context(), fixture.operation))
	assert.Equal(t, []StageName{StageArtifact, StageRuntimePull, StageRender}, fixture.outbox.createdStages())
	assert.Equal(t, store.StatusQueued, fixture.operations.status)
	assert.Equal(t, LifecyclePassed, fixture.lifecycle.overall)
	assert.Equal(t, "artifact,runtime_pull,render", fixture.lifecycle.stages)
}

func TestCoordinatorRunCancellationPersistsCancelled(t *testing.T) {
	fixture := newCoordinatorFixture(t, []StageDef{{Name: StageArtifact, Required: true, Timeout: time.Second}})
	fixture.outbox.block = true
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- fixture.coordinator.Run(ctx, fixture.operation) }()
	require.Eventually(t, func() bool { return len(fixture.outbox.createdStages()) == 1 }, time.Second, time.Millisecond)
	cancel()

	err := <-done
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, store.StatusPreflight, fixture.operations.status)
	assert.Equal(t, LifecycleCancelled, fixture.lifecycle.overall)
	assert.Equal(t, "artifact", fixture.lifecycle.stages)
}

func TestCoordinatorRunResumesExistingCommand(t *testing.T) {
	fixture := newCoordinatorFixture(t, []StageDef{{Name: StageArtifact, Required: true, Timeout: time.Second}})
	fixture.outbox.createErr = store.ErrDuplicateKey
	fixture.outbox.existingCommand = true

	require.NoError(t, fixture.coordinator.Run(t.Context(), fixture.operation))
	assert.Equal(t, store.StatusQueued, fixture.operations.status)
	assert.Equal(t, LifecyclePassed, fixture.lifecycle.overall)
	assert.Empty(t, fixture.outbox.createdStages())
	assert.Equal(t, "artifact", fixture.lifecycle.stages)
}

func TestCoordinatorRunStageTimeoutPersistsCancelled(t *testing.T) {
	fixture := newCoordinatorFixture(t, []StageDef{{Name: StageArtifact, Required: true, Timeout: time.Millisecond}})
	fixture.outbox.block = true

	err := fixture.coordinator.Run(t.Context(), fixture.operation)
	require.ErrorContains(t, err, "timed out")
	assert.Equal(t, store.StatusFailed, fixture.operations.status)
	assert.Equal(t, LifecycleCancelled, fixture.lifecycle.overall)
	assert.Equal(t, "artifact", fixture.lifecycle.stages)
}

func TestCoordinatorRunCancellationDuringTransitionPersistsCancelled(t *testing.T) {
	fixture := newCoordinatorFixture(t, []StageDef{{Name: StageArtifact, Required: true, Timeout: time.Second}})
	fixture.outbox.results[StageArtifact] = StageResult{Status: StageFailed, Detail: "rejected"}
	ctx, cancel := context.WithCancel(t.Context())
	fixture.operations.update = func() error {
		cancel()
		return context.Canceled
	}

	err := fixture.coordinator.Run(ctx, fixture.operation)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, store.StatusPreflight, fixture.operations.status)
	assert.Equal(t, LifecycleCancelled, fixture.lifecycle.overall)
	assert.Equal(t, "artifact", fixture.lifecycle.stages)
}

func TestCoordinatorRunStartFailureDispatchesNothing(t *testing.T) {
	fixture := newCoordinatorFixture(t, []StageDef{{Name: StageArtifact, Required: true, Timeout: time.Second}})
	fixture.lifecycle.createErr = errors.New("store unavailable")

	err := fixture.coordinator.Run(t.Context(), fixture.operation)
	require.ErrorContains(t, err, "record preflight start")
	assert.Empty(t, fixture.outbox.createdStages())
	assert.Zero(t, fixture.lifecycle.updateCalls)
}

func TestCoordinatorRunFinalizeFailureIsReturned(t *testing.T) {
	fixture := newCoordinatorFixture(t, nil)
	fixture.lifecycle.updateErr = errors.New("finalize unavailable")

	err := fixture.coordinator.Run(t.Context(), fixture.operation)
	require.ErrorContains(t, err, "record preflight result")
	assert.Equal(t, store.StatusQueued, fixture.operations.status)
}

func TestCoordinatorRunQueuedRaceDoesNotPersistPassed(t *testing.T) {
	fixture := newCoordinatorFixture(t, []StageDef{{Name: StageArtifact, Required: true, Timeout: time.Second}})
	fixture.operations.updateErr = store.ErrOptimisticLock

	err := fixture.coordinator.Run(t.Context(), fixture.operation)
	require.ErrorIs(t, err, store.ErrOptimisticLock)
	assert.Equal(t, LifecycleFailed, fixture.lifecycle.overall)
	assert.Equal(t, "artifact", fixture.lifecycle.stages)
}

func TestCoordinatorRunFailedRaceKeepsLifecycleFailed(t *testing.T) {
	fixture := newCoordinatorFixture(t, []StageDef{{Name: StageArtifact, Required: true, Timeout: time.Second}})
	fixture.outbox.results[StageArtifact] = StageResult{Status: StageFailed, Detail: "rejected"}
	fixture.operations.updateErr = store.ErrOptimisticLock

	err := fixture.coordinator.Run(t.Context(), fixture.operation)
	require.ErrorIs(t, err, store.ErrOptimisticLock)
	assert.Equal(t, LifecycleFailed, fixture.lifecycle.overall)
	assert.Equal(t, "artifact", fixture.lifecycle.stages)
}

type coordinatorFixture struct {
	coordinator *Coordinator
	operation   *store.Operation
	outbox      *coordinatorOutboxStore
	operations  *coordinatorOperationStore
	operators   *coordinatorOperatorStore
	lifecycle   *coordinatorLifecycleStore
}

func newCoordinatorFixture(t *testing.T, stages []StageDef) coordinatorFixture {
	t.Helper()
	outbox := &coordinatorOutboxStore{results: make(map[StageName]StageResult)}
	operations := &coordinatorOperationStore{status: store.StatusPreflight, version: 1}
	operators := &coordinatorOperatorStore{items: []*store.Operator{{ID: "operator-1", Status: store.OperatorActive}}}
	lifecycle := &coordinatorLifecycleStore{}
	coordinator := NewCoordinator(
		outbox,
		operations,
		operators,
		&stubDefinitionStore{def: &store.ReleaseDefinition{ID: "definition-1", ClusterID: "cluster-1"}},
		lifecycle,
		slog.New(slog.DiscardHandler),
	)
	coordinator.stages = stages
	coordinator.pollInterval = time.Millisecond
	coordinator.finalizeTimeout = time.Second
	return coordinatorFixture{
		coordinator: coordinator,
		operation: &store.Operation{
			ID: "operation-1", OperationType: store.OperationInstall, Status: store.StatusPreflight,
			ReleaseDefinitionID: "definition-1", StateVersion: 1,
		},
		outbox: outbox, operations: operations, operators: operators, lifecycle: lifecycle,
	}
}

type coordinatorLifecycleStore struct {
	mu          sync.Mutex
	createErr   error
	updateErr   error
	createCalls int
	updateCalls int
	overall     string
	stages      string
}

func (s *coordinatorLifecycleStore) CreateOrReset(_ context.Context, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createCalls++
	return s.createErr
}

func (s *coordinatorLifecycleStore) UpdateResult(_ context.Context, _, overall, stages string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateCalls++
	s.overall = overall
	s.stages = stages
	return s.updateErr
}

func (s *coordinatorLifecycleStore) DeleteExpired(context.Context, time.Duration) (int64, error) {
	return 0, nil
}

type coordinatorOutboxStore struct {
	mu              sync.Mutex
	entries         []*store.OutboxEntry
	results         map[StageName]StageResult
	block           bool
	createErr       error
	existingCommand bool
}
func (s *coordinatorOutboxStore) Create(_ context.Context, entry *store.OutboxEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	copyEntry := *entry
	s.entries = append(s.entries, &copyEntry)
	return nil
}

func (s *coordinatorOutboxStore) GetByCommandID(ctx context.Context, commandID string) (*store.OutboxEntry, error) {
	s.mu.Lock()
	hasEntry := s.existingCommand
	for _, entry := range s.entries {
		if entry.CommandID == commandID {
			hasEntry = true
			break
		}
	}
	s.mu.Unlock()
	if !hasEntry {
		return nil, store.ErrNotFound
	}
	if s.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	stage := StageName(commandID[len("operation-1:"):])
	result := s.results[stage]
	if result.Status == "" {
		result.Status = StagePassed
	}
	encoded, err := jsonMarshal(result)
	if err != nil {
		return nil, err
	}
	return &store.OutboxEntry{CommandID: commandID, Status: store.CommandPersisted, ResultJSON: encoded}, nil
}

func (s *coordinatorOutboxStore) createdStages() []StageName {
	s.mu.Lock()
	defer s.mu.Unlock()
	stages := make([]StageName, 0, len(s.entries))
	for _, entry := range s.entries {
		stages = append(stages, StageName(entry.CommandID[len("operation-1:"):]))
	}
	return stages
}

func (s *coordinatorOutboxStore) Get(context.Context, string) (*store.OutboxEntry, error) {
	return nil, store.ErrNotFound
}
func (s *coordinatorOutboxStore) GetPendingForOperator(context.Context, string) (*store.OutboxEntry, error) {
	return nil, store.ErrNotFound
}
func (s *coordinatorOutboxStore) GetDeliveredNotAcked(context.Context, string) ([]*store.OutboxEntry, error) {
	return nil, nil
}
func (s *coordinatorOutboxStore) GetInflightForOperator(context.Context, string) (*store.OutboxEntry, error) {
	return nil, store.ErrNotFound
}
func (s *coordinatorOutboxStore) GetNextSequence(context.Context) (int64, error)      { return 0, nil }
func (s *coordinatorOutboxStore) UpdateSequence(context.Context, string, int64) error { return nil }
func (s *coordinatorOutboxStore) UpdateStatus(context.Context, string, store.CommandStatus, string) error {
	return nil
}
func (s *coordinatorOutboxStore) GetNextPending(context.Context, string) (*store.OutboxEntry, error) {
	return nil, store.ErrNotFound
}

type coordinatorOperationStore struct {
	mu        sync.Mutex
	status    store.OperationStatus
	version   int
	updateErr error
	update    func() error
}

func (s *coordinatorOperationStore) UpdateStatus(_ context.Context, _ string, status store.OperationStatus, version int, _ string) (*store.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.update != nil {
		if err := s.update(); err != nil {
			return nil, err
		}
	}
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if version != s.version {
		return nil, store.ErrOptimisticLock
	}
	s.status = status
	s.version++
	return &store.Operation{Status: s.status, StateVersion: s.version}, nil
}

func (s *coordinatorOperationStore) Create(context.Context, *store.Operation) error { return nil }
func (s *coordinatorOperationStore) CreateIfAvailable(context.Context, *store.Operation) error {
	return nil
}
func (s *coordinatorOperationStore) Get(context.Context, string) (*store.Operation, error) {
	return nil, store.ErrNotFound
}
func (s *coordinatorOperationStore) GetByIdempotencyKey(context.Context, string) (*store.Operation, error) {
	return nil, store.ErrNotFound
}
func (s *coordinatorOperationStore) Transition(ctx context.Context, id string, status store.OperationStatus, version int, reason string) (*store.Operation, error) {
	return s.UpdateStatus(ctx, id, status, version, reason)
}
func (s *coordinatorOperationStore) GetCancelReplay(context.Context, store.OperationCancelReplayQuery) (*store.OperationCancelResult, error) {
	return nil, store.ErrNotFound
}
func (s *coordinatorOperationStore) Cancel(context.Context, store.OperationCancelCommand) (*store.OperationCancelResult, error) {
	return nil, store.ErrInvalidState
}
func (s *coordinatorOperationStore) HasActiveForDefinition(context.Context, string) (bool, error) {
	return false, nil
}
func (s *coordinatorOperationStore) HasActiveEmergencyForDefinition(context.Context, string) (bool, error) {
	return false, nil
}
func (s *coordinatorOperationStore) List(context.Context, string) ([]*store.Operation, error) {
	return nil, nil
}
func (s *coordinatorOperationStore) ListNonTerminal(context.Context) ([]*store.Operation, error) {
	return nil, nil
}

type coordinatorOperatorStore struct{ items []*store.Operator }

func (s *coordinatorOperatorStore) ListByCluster(context.Context, string) ([]*store.Operator, error) {
	return s.items, nil
}
func (s *coordinatorOperatorStore) Create(context.Context, *store.Operator) error { return nil }
func (s *coordinatorOperatorStore) Get(context.Context, string) (*store.Operator, error) {
	return nil, store.ErrNotFound
}
func (s *coordinatorOperatorStore) GetByCertSerial(context.Context, string) (*store.Operator, error) {
	return nil, store.ErrNotFound
}
func (s *coordinatorOperatorStore) GetByClusterID(context.Context, string) (*store.Operator, error) {
	return nil, store.ErrNotFound
}
func (s *coordinatorOperatorStore) GetByName(context.Context, string) (*store.Operator, error) {
	return nil, store.ErrNotFound
}
func (s *coordinatorOperatorStore) Update(context.Context, *store.Operator) error { return nil }
func (s *coordinatorOperatorStore) Revoke(context.Context, string) error          { return nil }
func (s *coordinatorOperatorStore) ListByCustomer(context.Context, string) ([]*store.Operator, error) {
	return s.items, nil
}

func jsonMarshal(result StageResult) (string, error) {
	encoded, err := json.Marshal(result)
	return string(encoded), err
}
