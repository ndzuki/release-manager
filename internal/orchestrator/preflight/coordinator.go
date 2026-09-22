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

	// pollInterval is the stage polling cadence; tests override it to avoid
	// real second-scale waits. Stage timeouts come from the StageDef itself.
	pollInterval time.Duration
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
	// ADR-024: the artifact preflight is executed by the operator, so the row
	// the operation-creation unit of work persists for it is a real stage
	// command, not a local record. It therefore carries the resolved
	// operator_id — the outbox's delivery filter (GetNextPending) — and the
	// stage payload the operator routes to its artifact executor.
	//
	// When no operator is available the id stays empty: the row is still the
	// durable record AC-067-13 requires, but it is not dispatchable, and the
	// pipeline fails closed with stage_unavailable (AC-019-02).
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
	stages := stagesForOperation(op)
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

		// AC-019-03/07: the operation may be cancelled between the loop-top
		// check and a failed stage result (e.g. a dispatch/operator lookup that
		// lost the ctx race). Treat it as cancelled so the lifecycle records
		// cancelled instead of failed and no stale CAS overwrites the operation.
		if ctx.Err() != nil {
			c.logger.Warn("preflight cancelled during stage", "op_id", op.ID, "stage", stage.Name)
			return StageCancelled, results
		}
		if result.Status == StageCancelled {
			// AC-019-03/07: operation cancelled — the operation was already CASed
			// to cancelled by CancelOperation; a stale state_version must not
			// overwrite it with failed.
			return StageCancelled, results
		}
		if result.Status == StageFailed || result.Status == StageTimeout {
			// ADR-025 (Plan A): "required" is a runtime property, not a static
			// one. A stage that RAN and failed must block, whatever its static
			// Required flag says — the old `!stage.Required -> continue`
			// shortcut is exactly what let a real runtime_pull failure through
			// (REQ-048). A stage that could not run reports StageSkipped
			// (handled below) instead of failing, so nothing is lost by
			// removing it.
			errorCode := errorCodeFromStatus(result)

			if result.Status == StageTimeout {
				// REQ-019: preflight timeout is a cancellation — record the
				// operation as cancelled, not failed (overall enum row:
				// "Operation 取消或 preflight 超时被取消").
				c.casCancelled(ctx, op, AggregateResult{
					OperationID: op.ID,
					Overall:     StageCancelled,
					FailedStage: stage.Name,
					Stages:      results,
					ErrorCode:   errorCode,
				})
				return StageCancelled, results
			}
			c.casFailed(ctx, op, AggregateResult{
				OperationID: op.ID,
				Overall:     StageFailed,
				FailedStage: stage.Name,
				Stages:      results,
				ErrorCode:   errorCode,
			})
			return StageFailed, results
		}
		if result.Status == StageSkipped {
			// The stage did not run (e.g. runtime_pull is not enabled in this
			// cluster): it is not a pass and not a failure. ADR-025 makes this
			// the only way a stage is allowed to be "not required".
			c.logger.Info("stage skipped, continuing",
				"op_id", op.ID, "stage", stage.Name, "detail", result.Detail)
			continue
		}
		c.logger.Info("stage passed", "op_id", op.ID, "stage", stage.Name)
	}

	// All stages passed. INSTALL/ROLLBACK still need their release write: the
	// preflight stage commands are checks now, so the write is a separate
	// non-stage command (TASK-114/U-1). Without it the operation would reach
	// succeeded with nothing installed.
	// D-γ / γ-1a: build the release write first (reads only), then commit the
	// queued transition and the dispatch in ONE transaction. A delivered command
	// must never exist for an operation whose queue transition did not commit.
	var (
		dispatch     *store.OutboxEntry
		dispatchErr  error
		dispatchCode = "dispatch_failed"
	)
	switch op.OperationType {
	case store.OperationInstall, store.OperationRollback:
		dispatch, dispatchErr = c.buildExecutionDispatch(ctx, op)
	case store.OperationUpgrade:
		// ADR-027: UPGRADE runs the full pipeline too (D-108 ①b retracted --
		// its cause, the poisoned first dispatch row, was removed by TASK-114's
		// stage dispatch), so its release write is dispatched here like
		// INSTALL's. It still uses the UPGRADE payload, hence a separate call.
		dispatch, dispatchCode, dispatchErr = c.buildUpgradeDispatch(ctx, op)
	}
	if dispatchErr != nil {
		c.logger.Error("execution dispatch failed", "op_id", op.ID, "err", dispatchErr)
		c.casFailed(ctx, op, AggregateResult{
			OperationID: op.ID,
			Overall:     StageFailed,
			FailedStage: executionStageName,
			Stages:      results,
			ErrorCode:   dispatchCode,
		})
		return StageFailed, results
	}

	// CAS to queued. The release write is now an ordinary command, so the
	// gateway's FinishOperation drives queued→running→succeeded/failed when its
	// result arrives — exactly the UPGRADE path (runUpgrade dispatches its
	// :execute entry and CASes queued the same way).
	result := AggregateResult{
		OperationID: op.ID,
		Overall:     StagePassed,
		Stages:      results,
	}
	if err := c.queueOperation(ctx, op, result, dispatch); err != nil {
		c.logger.Error("queue transition failed", "op_id", op.ID, "err", err)
		c.casFailed(ctx, op, AggregateResult{
			OperationID: op.ID,
			Overall:     StageFailed,
			FailedStage: executionStageName,
			Stages:      results,
			ErrorCode:   "dispatch_failed",
		})
		return StageFailed, results
	}
	return StagePassed, results
}

// executionStageName is the synthetic stage name used when the post-preflight
// release write cannot be dispatched. It is not a preflight stage; it exists so
// the failure is attributable in the persisted stage results.
const executionStageName StageName = "execute"

// stagesForOperation selects the preflight stages an operation has inputs for.
//
// The selection is driven by the operation's INPUTS, not by its type. Every
// production stage is chart-dependent: artifact verifies the bundle's chart
// archive digest (ADR-024), render/cluster render or dry-run the chart, and
// runtime_pull pulls it. An operation with no bundle therefore has nothing for
// any of them to check, and dispatching one anyway fails a *required* stage on
// an operation that has no chart at all (real CI run 2026-09-21: `render stage
// requires a bundle`; ADR-024's operator-side artifact check fails closed with
// `artifact stage requires a bundle`).
//
// ADR-024 made the artifact stage chart-dependent, so the rule is stated over
// the bundle and not over the operation type: a bundle-less operation of any
// type dispatches no stage, and an operation that carries a bundle keeps the
// full pipeline. Expressing it as "ROLLBACK runs the artifact stage only"
// (the pre-ADR-024 shape) would dispatch a stage that has no subject and would
// have to be re-decided for every future operation type.
//
// A ROLLBACK is the operation that exercises this today: it restores a revision
// Helm already stores, the orchestrator creates it without a bundle
// (rollback.go), and its real preconditions (active inventory, expected
// revision, no concurrent operation) are validated when it is created (REQ-067
// rule 13). It runs as a separate non-stage :execute command — the route
// AC-090-01 explicitly allows.
//
// INSTALL and the other staged operation types carry a bundle and keep the full
// pipeline.
func stagesForOperation(op *store.Operation) []StageDef {
	if op == nil || op.BundleID == "" {
		return nil
	}
	return ProductionStages()
}

// dispatchExecution writes the real release write for an INSTALL/ROLLBACK
// operation after every preflight stage passed.
//
// The command deliberately carries no stage: the operator's stage dispatcher
// routes every stage-typed command to a check and fails closed for one it does
// not know, so the release write must be an ordinary command (TASK-114 AC 2).
// The command id is stable so a resumed run reuses the existing row (D-87).
func (c *Coordinator) buildExecutionDispatch(ctx context.Context, op *store.Operation) (*store.OutboxEntry, error) {
	commandID := op.ID + ":execute"
	if _, err := c.outbox.GetByCommandID(ctx, commandID); err == nil {
		c.logger.Debug("consuming existing execution dispatch", "op_id", op.ID, "command_id", commandID)
		// Already dispatched: nothing to insert, but the queued transition still runs.
		return nil, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("execution dispatch lookup: %w", err)
	}

	operatorID, err := c.resolveOperator(ctx, op)
	if err != nil {
		return nil, err
	}
	var bundleProto *commonv1.ReleaseBundle
	var effective []byte
	if bundle, bundleErr := c.bundles.Get(ctx, op.BundleID); bundleErr == nil {
		bundleProto = bundleToProto(bundle)
		if op.ValuesRevisionID != "" {
			if revision, revErr := c.values.Get(ctx, op.ValuesRevisionID); revErr == nil {
				effective = revision.CanonicalDocument
			}
		}
	}
	payload, err := c.commandPayload(ctx, op, "", bundleProto, effective)
	if err != nil {
		return nil, err
	}
	encoded, err := payload.Marshal()
	if err != nil {
		return nil, fmt.Errorf("marshal execution payload: %w", err)
	}
	// Built, not written: queueOperation commits it with the queued transition.
	return &store.OutboxEntry{
		ID: uuid.NewString(), CommandID: commandID, OperationID: op.ID,
		OperationType: string(op.OperationType), OperatorID: operatorID, Payload: encoded,
	}, nil
}

// dispatchUpgrade writes the UPGRADE release write. ADR-027 restored UPGRADE to
// the full preflight pipeline, so this runs after the stages pass -- like
// dispatchExecution for INSTALL/ROLLBACK -- and the pipeline owns the queued CAS
// (this function no longer CASes). The error code is returned rather than applied
// so the pipeline reports the same specific codes the old short-circuit did.
func (c *Coordinator) buildUpgradeDispatch(ctx context.Context, op *store.Operation) (*store.OutboxEntry, string, error) {
	operatorID, err := c.resolveOperator(ctx, op)
	if err != nil {
		return nil, "stage_unavailable", err
	}
	definition, err := c.defs.Get(ctx, op.ReleaseDefinitionID)
	if err != nil {
		return nil, "release_not_found", err
	}
	if _, err := c.invs.GetByDefinition(ctx, op.ReleaseDefinitionID); err != nil {
		return nil, "release_not_found", err
	}
	bundle, err := c.bundles.Get(ctx, op.BundleID)
	if err != nil {
		return nil, "bundle_not_found", err
	}
	revision, err := c.values.Get(ctx, op.ValuesRevisionID)
	if err != nil {
		return nil, "revision_not_approved", err
	}
	commandID := op.ID + ":execute"
	payload, err := BuildUpgradePayload(op, definition, bundle, revision, commandID)
	if err != nil {
		return nil, "render_failed", err
	}
	encoded, err := payload.Marshal()
	if err != nil {
		return nil, "invalid_command", err
	}
	// Built, not written: queueOperation commits it with the queued transition.
	return &store.OutboxEntry{
		ID: uuid.NewString(), CommandID: commandID, OperationID: op.ID,
		OperationType: string(store.OperationUpgrade), OperatorID: operatorID, Payload: encoded,
	}, "", nil
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

	// Load the bundle and effective values so EVERY stage command carries
	// the full execution context. The wire Command does not carry the stage,
	// and the operator executes each INSTALL-typed stage command against the
	// bundle — a stage dispatched without it fails `chart_ref is required`
	// (real smoke 2026-08-27: the render stage rejected a nil bundle). A
	// missing bundle is tolerated (fall back to nil) for operations without
	// one.
	var bundleProto *commonv1.ReleaseBundle
	var effective []byte
	if bundle, bundleErr := c.bundles.Get(ctx, op.BundleID); bundleErr == nil {
		bundleProto = bundleToProto(bundle)
		// The values passed with the stage command are the approved revision's
		// canonical document (the operator applies the bundle image overrides
		// itself during install — real smoke 2026-08-27: mergeEffectiveValues
		// here failed `values_path "image.repository" does not reference an
		// object` when the fixture values lacked an image object).
		if op.ValuesRevisionID != "" {
			if revision, revErr := c.values.Get(ctx, op.ValuesRevisionID); revErr == nil {
				effective = revision.CanonicalDocument
			}
		}
	}

	commandID := fmt.Sprintf("%s:%s", op.ID, stage.Name)

	// D-87/ADR-005: reuse an existing row for the same command identity. The
	// first artifact command is persisted atomically inside the operation
	// creation transaction (REQ-067 OperationCreationUnitOfWork); any stage
	// row left by a previous Run before an interruption is likewise resumed
	// instead of duplicated. The stable command_id makes restarts idempotent.
	if existing, err := c.outbox.GetByCommandID(ctx, commandID); err == nil {
		// A reused row must be deliverable. The outbox delivers by operator_id
		// (GetNextPending filters on it), so the pre-created artifact row
		// persisted while no operator was available (AC-067-13) can never reach
		// an operator. Polling it would burn the whole stage timeout and report
		// a timeout instead of the real cause, so fail closed with the code the
		// missing-operator path already uses.
		if existing.OperatorID == "" {
			return StageResult{
				Stage:  stage.Name,
				Status: StageFailed,
				Detail: "stage_unavailable: dispatch has no operator",
			}, nil
		}
		c.logger.Debug("consuming existing precheck dispatch",
			"op_id", op.ID, "command_id", commandID, "entry_id", existing.ID)
		return c.pollStage(ctx, commandID, stage)
	} else if !errors.Is(err, store.ErrNotFound) {
		return StageResult{
			Stage:  stage.Name,
			Status: StageFailed,
			Detail: fmt.Sprintf("dispatch lookup error: %v", err),
		}, err
	}

	payload, err := c.commandPayload(ctx, op, stage.Name, bundleProto, effective)
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
		// INSTALL creates the target namespace (the E2E fixture namespaces do
		// not pre-exist in the customer clusters; real smoke 2026-08-27:
		// namespaces "e2e-release" not found).
		CreateNamespace: op.OperationType == store.OperationInstall,
		TimeoutSeconds:  c.timeoutSeconds,
	}, nil
}

// pollStage waits for the operator to persist the stage result.
func (c *Coordinator) pollStage(ctx context.Context, commandID string, stage StageDef) (StageResult, error) {
	stageCtx, cancel := context.WithTimeout(ctx, stage.Timeout)
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
			case store.CommandPersisted, store.CommandSucceeded:
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
				// The operator's command result writes a helm-shaped JSON
				// (`{"status":"succeeded",...}`); normalize a successful
				// execution to StagePassed (real smoke 2026-08-27: the render
				// stage's CommandSucceeded result was never consumed and the
				// stage timed out).
				if result.Status == "" || result.Status == "succeeded" || result.Status == "passed" {
					result.Status = StagePassed
				}
				return result, nil

			case store.CommandFailed:
				return failedStageResult(stage.Name, entry.ResultJSON), fmt.Errorf("command %s failed", commandID)

			default:
				// pending, delivered, running → continue polling
			}
		}
	}
}

// failedStageResult builds a failed stage result from the operator's raw result
// JSON.
//
// The operator reports a stable failure code in its result's `detail`
// (TASK-114), so prefer that over the whole JSON: errorCodeFromStatus would
// otherwise split the JSON at its first colon and record a meaningless code.
func failedStageResult(stage StageName, resultJSON string) StageResult {
	failed := StageResult{Stage: stage, Status: StageFailed, Detail: resultJSON}
	var reported StageResult
	if resultJSON != "" &&
		json.Unmarshal([]byte(resultJSON), &reported) == nil &&
		reported.Detail != "" {
		failed.Detail = reported.Detail
	}
	return failed
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

	// All operators revoked: fail closed (AC-019-02) — a revoked-only cluster
	// must never receive commands.

	return "", fmt.Errorf("no operator for cluster %s", def.ClusterID)
}

// casFailed transitions the operation to failed via EventError.
// persistPreflightResult records the stage results so the detail page can show
// which stage failed and what its checks said (TASK-149 / REQ-056 AC-056-03).
//
// Best effort by design: the operation's own transition is the contract, and the
// detail page degrades to the flat last_error if this write is lost. Failing the
// preflight because a diagnostic could not be stored would be worse.
func (c *Coordinator) persistPreflightResult(ctx context.Context, op *store.Operation, result AggregateResult) {
	encoded, err := json.Marshal(result)
	if err != nil {
		c.logger.Error("encode preflight result", "op_id", op.ID, "err", err)
		return
	}
	if err := c.ops.SavePreflightResult(ctx, op.ID, encoded); err != nil {
		c.logger.Error("persist preflight result", "op_id", op.ID, "err", err)
	}
}

func (c *Coordinator) casFailed(ctx context.Context, op *store.Operation, result AggregateResult) {
	c.logger.Error("preflight failed",
		"op_id", op.ID,
		"failed_stage", result.FailedStage,
		"error_code", result.ErrorCode,
	)

	c.persistPreflightResult(ctx, op, result)
	_, err := c.ops.UpdateStatus(ctx, op.ID, store.StatusFailed, op.StateVersion, result.ErrorCode)
	if err != nil {
		c.logger.Error("CAS failed transition failed", "op_id", op.ID, "err", err)
	}
}

// casCancelled transitions the operation to cancelled via EventCancel.
// Preflight timeout is a cancellation per REQ-019 ("Operation 取消或 preflight
// 超时被取消"); if CancelOperation already CASed the operation with a newer
// state_version, this CAS fails safely without overwriting it (AC-019-07).
func (c *Coordinator) casCancelled(ctx context.Context, op *store.Operation, result AggregateResult) {
	next, err := operation.Transition(op.Status, operation.EventCancel)
	if err != nil {
		c.logger.Error("preflight→cancelled transition invalid", "op_id", op.ID, "err", err)
		return
	}
	_, err = c.ops.UpdateStatus(ctx, op.ID, next, op.StateVersion, result.ErrorCode)
	if err != nil {
		c.logger.Error("CAS cancelled transition failed", "op_id", op.ID, "err", err)
	}
}

// queueOperation commits the transition to queued and the release write in one
// transaction (D-γ / γ-1a), then records the preflight result. Splitting the CAS
// from the dispatch -- the previous shape -- left a window where a delivered
// command belonged to an operation that was still in preflight, and
// FinishOperation drops a result for a non-queued operation, so the release
// could be applied without the operation ever recording it.
func (c *Coordinator) queueOperation(ctx context.Context, op *store.Operation, result AggregateResult, dispatch *store.OutboxEntry) error {
	c.persistPreflightResult(ctx, op, result)
	c.logger.Info("preflight passed, enqueuing operation", "op_id", op.ID)

	next, err := operation.Transition(op.Status, operation.EventPreflightPassed)
	if err != nil {
		return fmt.Errorf("preflight→queued transition invalid: %w", err)
	}
	return c.ops.QueueOperation(ctx, store.OperationQueueRequest{
		OperationID:  op.ID,
		NextStatus:   next,
		StateVersion: op.StateVersion,
		Dispatch:     dispatch,
	})
}

// startLifecycle records the running state before any dispatch (REQ-019 Phase Start).
func (c *Coordinator) startLifecycle(ctx context.Context, operationID string) error {
	if c.pl == nil {
		return nil
	}
	// Phase Start is authoritative persistence: a concurrently cancelled Run
	// must still record the running row so the final cancelled result has a
	// lifecycle to update (AC-019-05/07). Use a bounded non-cancelled context,
	// mirroring finalizeLifecycle.
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := c.pl.CreateOrReset(startCtx, operationID); err != nil {
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
