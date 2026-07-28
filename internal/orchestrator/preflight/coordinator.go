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
	"github.com/ndzuki/release-manager/internal/orchestrator/operation"
	"github.com/ndzuki/release-manager/internal/store"
)

// Coordinator orchestrates the sequential preflight pipeline for a release operation.
// It dispatches PRECHECK commands via the outbox, polls for results, and CAS the
// operation to queued (all passed) or failed (any required stage failed).
type Coordinator struct {
	outbox          store.OutboxStore
	ops             store.OperationStore
	opers           store.OperatorStore
	defs            store.DefinitionStore
	pl              store.PreflightLifecycleStore
	logger          *slog.Logger
	stages          []StageDef
	timeoutSeconds  int64
	pollInterval    time.Duration
	finalizeTimeout time.Duration
}

// NewCoordinator creates a preflight coordinator with the required store dependencies.
func NewCoordinator(
	outbox store.OutboxStore,
	ops store.OperationStore,
	opers store.OperatorStore,
	defs store.DefinitionStore,
	pl store.PreflightLifecycleStore,
	logger *slog.Logger,
) *Coordinator {
	return &Coordinator{
		outbox:          outbox,
		ops:             ops,
		opers:           opers,
		defs:            defs,
		pl:              pl,
		logger:          logger,
		stages:          ProductionStages(),
		timeoutSeconds:  int64((5 * time.Minute) / time.Second),
		pollInterval:    500 * time.Millisecond,
		finalizeTimeout: 5 * time.Second,
	}
}

// Run executes the preflight pipeline for the given operation.
//
//nolint:gocyclo // Run keeps ordered fail-closed stage decisions and lifecycle finalization together.
func (c *Coordinator) Run(ctx context.Context, op *store.Operation) (runErr error) {
	c.logger.Info("preflight coordinator started",
		"op_id", op.ID,
		"type", op.OperationType,
	)
	if c.pl == nil {
		return errors.New("preflight lifecycle store is required")
	}

	if err := c.pl.CreateOrReset(ctx, op.ID); err != nil {
		return fmt.Errorf("record preflight start: %w", err)
	}

	overall := LifecycleFailed
	executed := make([]string, 0, len(c.stages))
	defer func() {
		finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.finalizeTimeout)
		defer cancel()
		if err := c.pl.UpdateResult(finalizeCtx, op.ID, overall, strings.Join(executed, ",")); err != nil {
			c.logger.Error("failed to update preflight lifecycle",
				"operation_id", op.ID,
				"overall", overall,
				"err", err,
			)
			if runErr == nil {
				runErr = fmt.Errorf("record preflight result: %w", err)
			}
		}
	}()

	results := make([]StageResult, 0, len(c.stages))
	for _, stage := range c.stages {
		if err := ctx.Err(); err != nil {
			overall = LifecycleCancelled
			c.logger.Warn("preflight cancelled via context", "op_id", op.ID, "stage", stage.Name)
			return err
		}

		result, err := c.runStage(ctx, op, stage)
		executed = append(executed, string(stage.Name))
		results = append(results, result)
		if err != nil {
			c.logger.Error("stage execution error",
				"op_id", op.ID,
				"stage", stage.Name,
				"err", err,
			)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			overall = LifecycleCancelled
			return ctxErr
		}
		if result.Status == StageTimeout && stage.Required {
			errorCode := errorCodeFromStatus(result)
			if err := c.casFailed(ctx, op, AggregateResult{
				OperationID: op.ID,
				Overall:     StageFailed,
				FailedStage: stage.Name,
				Stages:      results,
				ErrorCode:   errorCode,
			}); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					overall = LifecycleCancelled
					return ctxErr
				}
				return err
			}
			overall = LifecycleCancelled
			return fmt.Errorf("required preflight stage %s timed out", stage.Name)
		}
		if result.Status != StageFailed && result.Status != StageTimeout {
			continue
		}
		if !stage.Required {
			c.logger.Warn("optional preflight stage failed",
				"op_id", op.ID,
				"stage", stage.Name,
				"detail", result.Detail,
			)
			continue
		}
		errorCode := errorCodeFromStatus(result)
		if err := c.casFailed(ctx, op, AggregateResult{
			OperationID: op.ID,
			Overall:     StageFailed,
			FailedStage: stage.Name,
			Stages:      results,
			ErrorCode:   errorCode,
		}); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				overall = LifecycleCancelled
				return ctxErr
			}
			return err
		}
		overall = LifecycleFailed
		return fmt.Errorf("required preflight stage %s failed", stage.Name)
	}
	if err := c.casQueued(ctx, op, AggregateResult{
		OperationID: op.ID,
		Overall:     StagePassed,
		Stages:      results,
	}); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			overall = LifecycleCancelled
			return ctxErr
		}
		return err
	}
	overall = LifecyclePassed
	return nil
}

// runStage dispatches a PRECHECK command for one stage and polls for its result.
func (c *Coordinator) runStage(ctx context.Context, op *store.Operation, stage StageDef) (StageResult, error) {
	emptyResult := StageResult{Stage: stage.Name, Status: StageFailed}
	commandID := fmt.Sprintf("%s:%s", op.ID, stage.Name)
	if _, err := c.outbox.GetByCommandID(ctx, commandID); err == nil {
		c.logger.Info("precheck command already exists, resuming poll",
			"op_id", op.ID,
			"stage", stage.Name,
			"command_id", commandID,
		)
		return c.pollStage(ctx, commandID, stage)
	} else if !errors.Is(err, store.ErrNotFound) {
		return emptyResult, fmt.Errorf("lookup existing precheck command: %w", err)
	}


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
		if _, existingErr := c.outbox.GetByCommandID(ctx, commandID); existingErr != nil {
			return StageResult{
				Stage:  stage.Name,
				Status: StageFailed,
				Detail: fmt.Sprintf("dispatch error: %v", err),
			}, err
		}
		c.logger.Info("precheck command already exists, resuming poll",
			"op_id", op.ID,
			"stage", stage.Name,
			"command_id", commandID,
		)
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

	ticker := time.NewTicker(c.pollInterval)
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
			entry, err := c.outbox.GetByCommandID(stageCtx, commandID)
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

	for _, operator := range opers {
		if operator.Status == store.OperatorActive {
			return operator.ID, nil
		}
	}

	return "", fmt.Errorf("no active operator for cluster %s", def.ClusterID)
}

// casFailed transitions the operation to failed via EventError.
func (c *Coordinator) casFailed(ctx context.Context, op *store.Operation, result AggregateResult) error {
	c.logger.Error("preflight failed",
		"op_id", op.ID,
		"failed_stage", result.FailedStage,
		"error_code", result.ErrorCode,
	)

	if _, err := c.ops.UpdateStatus(ctx, op.ID, store.StatusFailed, op.StateVersion, result.ErrorCode); err != nil {
		if errors.Is(err, store.ErrOptimisticLock) || errors.Is(err, store.ErrInvalidState) {
			c.logger.Info("preflight failed transition lost terminal-state race", "op_id", op.ID, "err", err)
			return fmt.Errorf("preflight failed transition lost state race: %w", err)
		}
		return fmt.Errorf("update operation to failed: %w", err)
	}
	return nil
}
func (c *Coordinator) casQueued(ctx context.Context, op *store.Operation, _ AggregateResult) error {
	c.logger.Info("preflight passed, enqueuing operation", "op_id", op.ID)

	next, err := operation.Transition(op.Status, operation.EventPreflightPassed)
	if err != nil {
		return fmt.Errorf("preflight to queued transition: %w", err)
	}

	if _, err = c.ops.UpdateStatus(ctx, op.ID, next, op.StateVersion, ""); err != nil {
		if errors.Is(err, store.ErrOptimisticLock) || errors.Is(err, store.ErrInvalidState) {
			c.logger.Info("preflight queued transition lost terminal-state race", "op_id", op.ID, "err", err)
			return fmt.Errorf("preflight queued transition lost state race: %w", err)
		}
		return fmt.Errorf("update operation to queued: %w", err)
	}
	return nil
}

// errorCodeFromStatus maps a stage result status to a preflight error code.
func errorCodeFromStatus(result StageResult) string {
	switch result.Status {
	case StageFailed:
		return string(StageFailed)
	case StageTimeout:
		return string(StageTimeout)
	default:
		return result.Detail
	}
}
