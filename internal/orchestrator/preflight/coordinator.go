package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	commonv1 "github.com/ndzuki/release-manager/api/gen/common/v1"
	"github.com/ndzuki/release-manager/internal/orchestrator/operation"
	"github.com/ndzuki/release-manager/internal/store"
)

// Coordinator orchestrates the sequential preflight pipeline for a release operation.
// It dispatches PRECHECK commands via the outbox, polls for results, and CAS the
// operation to queued (all passed) or failed (any required stage failed).
type Coordinator struct {
	outbox         store.OutboxStore
	ops            store.OperationStore
	opers          store.OperatorStore
	defs           store.DefinitionStore
	values         store.ValuesStore
	bundles        store.BundleStore
	pl             store.PreflightLifecycleStore
	invs           store.InventoryStore
	logger         *slog.Logger
	timeoutSeconds int64

	// pollInterval and stageTimeout are timing knobs used by pollStage; tests
	// override them to avoid real minute-scale stage timeouts.
	pollInterval time.Duration
	stageTimeout time.Duration
}

// NewCoordinator creates a preflight coordinator with the required store dependencies.
func NewCoordinator(
	outbox store.OutboxStore,
	ops store.OperationStore,
	opers store.OperatorStore,
	defs store.DefinitionStore,
	values store.ValuesStore,
	bundles store.BundleStore,
	pl store.PreflightLifecycleStore,
	invs store.InventoryStore,
	logger *slog.Logger,
) *Coordinator {
	return &Coordinator{
		outbox:         outbox,
		ops:            ops,
		opers:          opers,
		defs:           defs,
		values:         values,
		bundles:        bundles,
		pl:             pl,
		invs:           invs,
		logger:         logger,
		timeoutSeconds: int64((5 * time.Minute) / time.Second),
	}
}

// Run executes the preflight pipeline for the given operation.
// It blocks until all stages complete or a required stage fails.
// The caller should invoke this in a goroutine with a background context
// derived from the request context (so cancellation propagates).
// Dispatch builds the first durable preflight command for an operation.
// Returns errNoOperator when no operator is available; the caller should persist
// the dispatch record for later assignment.
var errNoOperator = fmt.Errorf("no operator available")

func (c *Coordinator) Dispatch(ctx context.Context, op *store.Operation, bundle *commonv1.ReleaseBundle, values []byte) (*store.OutboxEntry, error) {
	stage := ProductionStages()[0]
	operatorID, dispatchErr := c.resolveOperator(ctx, op)
	if dispatchErr != nil {
		operatorID = ""
		dispatchErr = errNoOperator
	}
	payload, err := c.commandPayload(ctx, op, stage.Name, bundle, values)
	if err != nil {
		return nil, err
	}
	encoded, err := payload.Marshal()
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	return &store.OutboxEntry{
		ID: uuid.New().String(), CommandID: fmt.Sprintf("%s:%s", op.ID, stage.Name),
		OperationID: op.ID, OperationType: string(op.OperationType), OperatorID: operatorID, Payload: encoded,
	}, dispatchErr
}

func (c *Coordinator) Run(ctx context.Context, op *store.Operation) {
	c.logger.Info("preflight coordinator started",
		"op_id", op.ID,
		"type", op.OperationType,
	)

	// Phase Start (REQ-019): record the lifecycle as running before any dispatch.
	// A start-write failure must not dispatch commands (AC-019-05).
	if err := c.startLifecycle(ctx, op.ID); err != nil {
		c.logger.Error("preflight lifecycle start failed", "op_id", op.ID, "err", err)
		return
	}

	overall, results := c.runPipeline(ctx, op)

	// Phase Complete (REQ-019): persist the final result with a bounded
	// non-cancelled context so cancellation/shutdown cannot drop the write
	// (AC-019-06/07).
	c.finalizeLifecycle(ctx, op.ID, overall, results)
}

func (c *Coordinator) runPipeline(ctx context.Context, op *store.Operation) (StageStatus, []StageResult) {
	if op.OperationType == store.OperationUpgrade {
		overall := c.runUpgrade(ctx, op)
		return overall, nil
	}
	stages := ProductionStages()
	results := make([]StageResult, 0, len(stages))

	for _, stage := range stages {
		// AC-019-03: context cancellation propagates and terminates
		select {
		case <-ctx.Done():
			c.logger.Warn("preflight cancelled via context", "op_id", op.ID, "stage", stage.Name)
			// The operation was already CASed to cancelled by CancelOperation;
			// a stale state_version must not overwrite it with failed (AC-019-07).
			return StageCancelled, results
		default:
		}

		result, err := c.runStage(ctx, op, stage)
		if err != nil {
			c.logger.Error("stage execution error",
				"op_id", op.ID,
				"stage", stage.Name,
				"err", err,
			)
		}
		results = append(results, result)

		if result.Status == StageCancelled {
			// AC-019-03/07: operation cancelled — the operation was already CASed
			// to cancelled by CancelOperation; a stale state_version must not
			// overwrite it with failed.
			return StageCancelled, results
		}
		if result.Status == StageFailed || result.Status == StageTimeout {
			errorCode := errorCodeFromStatus(result)
			c.casFailed(ctx, op, AggregateResult{
				OperationID: op.ID,
				Overall:     StageFailed,
				FailedStage: stage.Name,
				Stages:      results,
				ErrorCode:   errorCode,
			})
			// AC-019-07: preflight timeout is recorded as cancelled.
			if result.Status == StageTimeout {
				return StageCancelled, results
			}
			return StageFailed, results
		}
		// Optional stage failure → skip, continue (policy recorded)
		c.logger.Info("optional stage skipped", "op_id", op.ID, "stage", stage.Name)
	}

	// All stages passed → CAS to queued.
	result := AggregateResult{
		OperationID: op.ID,
		Overall:     StagePassed,
		Stages:      results,
	}
	c.casQueued(ctx, op, result)
	return StagePassed, results
}
func (c *Coordinator) runUpgrade(ctx context.Context, op *store.Operation) StageStatus {
	operatorID, err := c.resolveOperator(ctx, op)
	if err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "stage_unavailable"})
		return StageFailed
	}
	definition, err := c.defs.Get(ctx, op.ReleaseDefinitionID)
	if err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "release_not_found"})
		return StageFailed
	}
	if _, err := c.invs.GetByDefinition(ctx, op.ReleaseDefinitionID); err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "release_not_found"})
		return StageFailed
	}
	bundle, err := c.bundles.Get(ctx, op.BundleID)
	if err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "bundle_not_found"})
		return StageFailed
	}
	revision, err := c.values.Get(ctx, op.ValuesRevisionID)
	if err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "revision_not_approved"})
		return StageFailed
	}
	commandID := op.ID + ":execute"
	payload, err := BuildUpgradePayload(op, definition, bundle, revision, commandID)
	if err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "render_failed"})
		return StageFailed
	}
	encoded, err := payload.Marshal()
	if err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "invalid_command"})
		return StageFailed
	}
	if err := c.outbox.Create(ctx, &store.OutboxEntry{
		ID: uuid.NewString(), CommandID: commandID, OperationID: op.ID,
		OperationType: string(store.OperationUpgrade), OperatorID: operatorID, Payload: encoded,
	}); err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "dispatch_failed"})
		return StageFailed
	}
	c.casQueued(ctx, op, AggregateResult{OperationID: op.ID, Overall: StagePassed})
	return StagePassed
}
// runStage dispatches a PRECHECK command for one stage and polls for its result.
func (c *Coordinator) runStage(ctx context.Context, op *store.Operation, stage StageDef) (StageResult, error) {
	emptyResult := StageResult{Stage: stage.Name, Status: StageFailed}

	// Resolve target operator
	operatorID, err := c.resolveOperator(ctx, op)
	if err != nil {
		// AC-019-02: required stage unavailable → fail closed
		return StageResult{
			Stage:  stage.Name,
			Status: StageFailed,
			Detail: fmt.Sprintf("stage_unavailable: %v", err),
		}, err
	}

	commandID := fmt.Sprintf("%s:%s", op.ID, stage.Name)

	// D-87: the first artifact command is persisted atomically inside the
	// operation creation transaction (REQ-067 OperationCreationUnitOfWork).
	// Consume that row instead of creating a duplicate; operations without a
	// pre-created first row (e.g. rollback) create one on demand. The same
	// command identity also makes restarts idempotent.
	if stage.Name == StageArtifact {
		if existing, err := c.outbox.GetByCommandID(ctx, commandID); err == nil {
			c.logger.Debug("consuming pre-created artifact dispatch",
				"op_id", op.ID, "command_id", commandID, "entry_id", existing.ID)
			return c.pollStage(ctx, commandID, stage)
		} else if !errors.Is(err, store.ErrNotFound) {
			return StageResult{
				Stage:  stage.Name,
				Status: StageFailed,
				Detail: fmt.Sprintf("dispatch lookup error: %v", err),
			}, err
		}
	}

	payload, err := c.commandPayload(ctx, op, stage.Name, nil, nil)
	if err != nil {
		return emptyResult, err
	}
	encoded, err := payload.Marshal()
	if err != nil {
		return emptyResult, fmt.Errorf("marshal payload: %w", err)
	}

	entry := &store.OutboxEntry{
		ID:            uuid.New().String(),
		CommandID:     commandID,
		OperationID:   op.ID,
		OperationType: string(op.OperationType),
		OperatorID:    operatorID,
		Payload:       encoded,
	}

	if err := c.outbox.Create(ctx, entry); err != nil {
		return StageResult{
			Stage:  stage.Name,
			Status: StageFailed,
			Detail: fmt.Sprintf("dispatch error: %v", err),
		}, err
	}

	c.logger.Debug("precheck command dispatched",
		"op_id", op.ID,
		"stage", stage.Name,
		"command_id", commandID,
	)

	// Poll for result with stage-level timeout
	return c.pollStage(ctx, commandID, stage)
}

func (c *Coordinator) commandPayload(
	ctx context.Context,
	op *store.Operation,
	stage StageName,
	bundle *commonv1.ReleaseBundle,
	values []byte,
) (*CommandPayload, error) {
	def, err := c.defs.Get(ctx, op.ReleaseDefinitionID)
	if err != nil {
		return nil, fmt.Errorf("definition lookup for command: %w", err)
	}
	return &CommandPayload{
		Stage: stage, OperationID: op.ID, BundleID: op.BundleID, DefinitionID: def.ID,
		Bundle: bundle, Namespace: def.Namespace, ReleaseName: def.ReleaseName, Values: values,
		ValuesRevisionID: op.ValuesRevisionID, ValuesPatch: op.ValuesPatch,
		ExpectedCurrentRevision: int64(op.ExpectedRevision), TargetRevision: int64(op.TargetRevision),
		TimeoutSeconds: c.timeoutSeconds,
	}, nil
}

// pollStage waits for the operator to persist the stage result.
func (c *Coordinator) pollStage(ctx context.Context, commandID string, stage StageDef) (StageResult, error) {
	timeout := stage.Timeout
	if c.stageTimeout > 0 {
		timeout = c.stageTimeout
	}
	stageCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	poll := c.pollInterval
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		select {
		case <-stageCtx.Done():
			// AC-019-03/07: distinguish operation cancellation from a stage
			// timeout so the lifecycle records cancelled, not a failure.
			if ctx.Err() != nil {
				return StageResult{
					Stage:  stage.Name,
					Status: StageCancelled,
					Detail: "preflight_cancelled",
				}, ctx.Err()
			}
			return StageResult{
				Stage:  stage.Name,
				Status: StageTimeout,
				Detail: "stage_timeout",
			}, stageCtx.Err()

		case <-ticker.C:
			entry, err := c.outbox.GetByCommandID(ctx, commandID)
			if err != nil {
				c.logger.Warn("poll command lookup failed, retrying",
					"command_id", commandID,
					"err", err,
				)
				continue
			}

			switch entry.Status {
			case store.CommandPersisted:
				var result StageResult
				if entry.ResultJSON != "" {
					if err := json.Unmarshal([]byte(entry.ResultJSON), &result); err != nil {
						c.logger.Warn("failed to parse stage result",
							"command_id", commandID,
							"err", err,
						)
						result.Status = StageFailed
						result.Detail = "unparseable result"
					}
				}
				result.Stage = stage.Name
				if result.Status == "" {
					result.Status = StagePassed
				}
				return result, nil

			case store.CommandFailed:
				return StageResult{
					Stage:  stage.Name,
					Status: StageFailed,
					Detail: entry.ResultJSON,
				}, fmt.Errorf("command %s failed", commandID)

			default:
				// pending, delivered, running → continue polling
			}
		}
	}
}

// resolveOperator finds an active operator for the operation's target cluster.
func (c *Coordinator) resolveOperator(ctx context.Context, op *store.Operation) (string, error) {
	def, err := c.defs.Get(ctx, op.ReleaseDefinitionID)
	if err != nil {
		return "", fmt.Errorf("definition lookup: %w", err)
	}

	opers, err := c.opers.ListByCluster(ctx, def.ClusterID)
	if err != nil {
		return "", fmt.Errorf("operator list: %w", err)
	}

	// Prefer active operator
	for _, o := range opers {
		if o.Status == store.OperatorActive {
			return o.ID, nil
		}
	}

	// Fallback: any non-revoked operator
	for _, o := range opers {
		if o.Status != store.OperatorRevoked {
			return o.ID, nil
		}
	}

	if len(opers) > 0 {
		return opers[0].ID, nil
	}

	return "", fmt.Errorf("no operator for cluster %s", def.ClusterID)
}

// casFailed transitions the operation to failed via EventError.
func (c *Coordinator) casFailed(ctx context.Context, op *store.Operation, result AggregateResult) {
	c.logger.Error("preflight failed",
		"op_id", op.ID,
		"failed_stage", result.FailedStage,
		"error_code", result.ErrorCode,
	)

	_, err := c.ops.UpdateStatus(ctx, op.ID, store.StatusFailed, op.StateVersion, result.ErrorCode)
	if err != nil {
		c.logger.Error("CAS failed transition failed", "op_id", op.ID, "err", err)
	}
}

func (c *Coordinator) casQueued(ctx context.Context, op *store.Operation, _ AggregateResult) {
	c.logger.Info("preflight passed, enqueuing operation", "op_id", op.ID)

	next, err := operation.Transition(op.Status, operation.EventPreflightPassed)
	if err != nil {
		c.logger.Error("preflight→queued transition invalid", "op_id", op.ID, "err", err)
		return
	}

	_, err = c.ops.UpdateStatus(ctx, op.ID, next, op.StateVersion, "")
	if err != nil {
		c.logger.Error("CAS queued transition failed", "op_id", op.ID, "err", err)
	}
}

// startLifecycle records the running state before any dispatch (REQ-019 Phase Start).
func (c *Coordinator) startLifecycle(ctx context.Context, operationID string) error {
	if c.pl == nil {
		return nil
	}
	if _, err := c.pl.CreateOrReset(ctx, operationID); err != nil {
		return fmt.Errorf("record preflight start: %w", err)
	}
	return nil
}

// finalizeLifecycle persists the final result (REQ-019 Phase Complete) using a
// bounded non-cancelled cleanup context so a cancelled Run cannot drop the write.
// Failures are logged but not propagated — lifecycle persistence is observational.
func (c *Coordinator) finalizeLifecycle(ctx context.Context, operationID string, overall StageStatus, stages []StageResult) {
	if c.pl == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := c.pl.UpdateResult(cleanupCtx, operationID, string(overall), canonicalStages(stages)); err != nil {
		c.logger.Warn("failed to update preflight lifecycle", "op_id", operationID, "err", err)
	}
}

// canonicalStageName maps a stage to its canonical lifecycle name (REQ-019):
// the cluster stage is recorded as "dryrun" matching the command type.
func canonicalStageName(s StageName) string {
	if s == StageCluster {
		return "dryrun"
	}
	return string(s)
}

// canonicalStages returns the comma-separated canonical stage names in execution
// order (REQ-019 stages construction).
func canonicalStages(stages []StageResult) string {
	names := make([]string, 0, len(stages))
	for _, s := range stages {
		names = append(names, canonicalStageName(s.Stage))
	}
	return strings.Join(names, ",")
}

// errorCodeFromStatus maps a stage result status to a preflight error code.
func errorCodeFromStatus(result StageResult) string {
	if result.Detail != "" {
		if code, _, ok := strings.Cut(result.Detail, ":"); ok {
			return strings.TrimSpace(code)
		}
		return result.Detail
	}
	if result.Status == StageTimeout {
		return "stage_timeout"
	}
	return "preflight_failed"
}
