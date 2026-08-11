package preflight

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
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
func (c *Coordinator) Run(ctx context.Context, op *store.Operation) {
	c.logger.Info("preflight coordinator started",
		"op_id", op.ID,
		"type", op.OperationType,
	)
	if op.OperationType == store.OperationUpgrade {
		c.runUpgrade(ctx, op)
		return
	}

	stages := ProductionStages()
	results := make([]StageResult, 0, len(stages))

	for _, stage := range stages {
		// AC-019-03: context cancellation propagates and terminates
		select {
		case <-ctx.Done():
			c.logger.Warn("preflight cancelled via context", "op_id", op.ID, "stage", stage.Name)
			c.casFailed(ctx, op, AggregateResult{
				OperationID: op.ID,
				Overall:     StageFailed,
				FailedStage: stage.Name,
				Stages:      results,
				ErrorCode:   "preflight_cancelled",
			})
			return
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

		if result.Status == StageFailed || result.Status == StageTimeout {
			errorCode := errorCodeFromStatus(result)
			c.casFailed(ctx, op, AggregateResult{
				OperationID: op.ID,
				Overall:     StageFailed,
				FailedStage: stage.Name,
				Stages:      results,
				ErrorCode:   errorCode,
			})
			// Record lifecycle result for GC (REQ-069).
			c.recordLifecycle(ctx, op.ID, results, string(StageFailed), errorCode)
			return
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
	// Record lifecycle result for GC (REQ-069).
	c.recordLifecycle(ctx, op.ID, results, string(StagePassed), "")
}
func (c *Coordinator) runUpgrade(ctx context.Context, op *store.Operation) {
	operatorID, err := c.resolveOperator(ctx, op)
	if err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "stage_unavailable"})
		return
	}
	definition, err := c.defs.Get(ctx, op.ReleaseDefinitionID)
	if err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "release_not_found"})
		return
	}
	if _, err := c.invs.GetByDefinition(ctx, op.ReleaseDefinitionID); err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "release_not_found"})
		return
	}
	bundle, err := c.bundles.Get(ctx, op.BundleID)
	if err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "bundle_not_found"})
		return
	}
	revision, err := c.values.Get(ctx, op.ValuesRevisionID)
	if err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "revision_not_approved"})
		return
	}
	commandID := op.ID + ":execute"
	payload, err := BuildUpgradePayload(op, definition, bundle, revision, commandID)
	if err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "render_failed"})
		return
	}
	encoded, err := payload.Marshal()
	if err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "invalid_command"})
		return
	}
	if err := c.outbox.Create(ctx, &store.OutboxEntry{
		ID: uuid.NewString(), CommandID: commandID, OperationID: op.ID,
		OperationType: string(store.OperationUpgrade), OperatorID: operatorID, Payload: encoded,
	}); err != nil {
		c.casFailed(ctx, op, AggregateResult{OperationID: op.ID, Overall: StageFailed, ErrorCode: "dispatch_failed"})
		return
	}
	c.casQueued(ctx, op, AggregateResult{OperationID: op.ID, Overall: StagePassed})
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

	// Build and dispatch command.
	commandID := fmt.Sprintf("%s:%s", op.ID, stage.Name)
	payload, err := c.commandPayload(ctx, op, stage)
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
		OperationType: CommandType(stage.Name),
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
	stage StageDef,
) (*CommandPayload, error) {
	def, err := c.defs.Get(ctx, op.ReleaseDefinitionID)
	if err != nil {
		return nil, fmt.Errorf("definition lookup for command: %w", err)
	}
	payload := &CommandPayload{
		Stage:                   stage.Name,
		OperationID:             op.ID,
		BundleID:                op.BundleID,
		DefinitionID:            def.ID,
		Namespace:               def.Namespace,
		ReleaseName:             def.ReleaseName,
		TimeoutSeconds:          c.timeoutSeconds,
		ValuesRevisionID:        op.ValuesRevisionID,
		ExpectedCurrentRevision: int64(op.ExpectedRevision),
		TargetRevision:          int64(op.TargetRevision),
		Atomic:                  op.OperationType == store.OperationInstall || op.OperationType == store.OperationUpgrade,
		ValuesPatch:             op.ValuesPatch,
	}
	return payload, nil
}

// pollStage waits for the operator to persist the stage result.
func (c *Coordinator) pollStage(ctx context.Context, commandID string, stage StageDef) (StageResult, error) {
	stageCtx, cancel := context.WithTimeout(ctx, stage.Timeout)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-stageCtx.Done():
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

// recordLifecycle persists a PreflightLifecycle record for GC (REQ-069).
// This is best-effort — failures are logged but not propagated.
func (c *Coordinator) recordLifecycle(ctx context.Context, operationID string, stages []StageResult, overall, errorCode string) {
	if c.pl == nil {
		return
	}

	stagesJSON, err := json.Marshal(stages)
	if err != nil {
		c.logger.Warn("failed to marshal preflight stages for lifecycle", "op_id", operationID, "err", err)
		return
	}

	opID := &operationID
	pl := &store.PreflightLifecycle{
		OperationID: opID,
		Stages:      stagesJSON,
		Overall:     overall,
		ErrorCode:   errorCode,
	}
	if err := c.pl.Create(ctx, pl); err != nil {
		c.logger.Warn("failed to record preflight lifecycle", "op_id", operationID, "err", err)
	}
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
