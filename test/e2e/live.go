package e2e

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
)

// LiveRecovery implements Recovery over the formal Connect surface using the
// e2e-runner development account (REQ-065). It performs no kubectl/helm/db
// access; generated clients and the access token remain private to the session.
type LiveRecovery struct {
	clients *ClientBundle
	session *RunnerSession
	// scope identifies this recovery invocation. Recovery writes are new logical
	// operations each time cleanup runs: the next run changes the same definition
	// and workload again, so a key built from the target alone replays the
	// previous invocation's terminal operation and restores nothing (real smoke
	// 2026-09-11: cleanup logged the replica restore while the workload stayed at
	// the count the run had left behind).
	scope string
}

// NewLiveRecovery constructs the formal-API recovery implementation. The
// config must already be validated (LoadConfig resolves the password).
func NewLiveRecovery(cfg *Config, clients *ClientBundle) (*LiveRecovery, error) {
	session, err := NewRunnerSession(cfg, clients)
	if err != nil {
		return nil, err
	}
	return &LiveRecovery{clients: clients, session: session, scope: newRecoveryScope()}, nil
}

// newRecoveryScope returns a value that is fixed for one recovery invocation and
// distinct from every other. It is computed once per invocation, so a retry
// inside the invocation still dedupes onto one key.
func newRecoveryScope() string {
	return strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
}

// idempotencyKey renders this invocation's key for one recovery write.
func (r *LiveRecovery) idempotencyKey(kind, target string) string {
	return idempotencyKey(kind, r.scope, target)
}

// Login authenticates as e2e-runner and resolves the authoritative user id.
func (r *LiveRecovery) Login(ctx context.Context) (string, error) {
	if r == nil || r.clients == nil {
		return "", ErrRunnerLogin
	}
	return r.session.Login(ctx)
}

// UserID returns the authenticated runner user id after a successful Login.
func (r *LiveRecovery) UserID() string {
	if r == nil {
		return ""
	}
	return r.session.UserID()
}

// token returns the current bearer token (empty before Login).
func (r *LiveRecovery) token() string {
	if r == nil {
		return ""
	}
	return r.session.Token()
}

// ListReleaseInventory implements Recovery.
//
// The read re-attempts while the server reports a condition a later attempt can
// clear. It has to: the orchestrator answers ListReleaseInventory with CodeInternal
// for every row while any definition has a non-terminal operation, because the
// Postgres GetActiveForDefinition still selects the pre-emergency 16-column set
// while the shared scan reads 30 (the SQLite twin was updated; Postgres was not).
// That state is one cleanup manufactures itself -- its own rollback is an
// operation in flight -- so without this the second cleanup of a pair failed with
// "list release inventory: internal: internal error" on an environment the first
// one had just restored. A bounded retry is the runner-side answer; the engine
// divergence belongs to the store and is recorded in D-033 rather than patched
// from here.
//
// Retrying is safe because the read only observes. The retry lives here rather
// than at the call sites so that cleanup, the baseline sample, and the post-run
// residue sample all inherit it and none can be left on the failing path.
func (r *LiveRecovery) ListReleaseInventory(ctx context.Context) ([]CleanupRow, error) {
	response, err := retryInventoryRead(ctx, func(ctx context.Context) (*orchestratorv1.ListReleaseInventoryResponse, error) {
		result, err := r.clients.orchestrator.ListReleaseInventory(ctx, authorizedRequest(r.token(), &orchestratorv1.ListReleaseInventoryRequest{}))
		if err != nil {
			return nil, err
		}
		return result.Msg, nil
	})
	if err != nil {
		return nil, err
	}
	rows := make([]CleanupRow, 0, len(response.Rows))
	for _, row := range response.Rows {
		if row == nil {
			continue
		}
		item := CleanupRow{
			CustomerID:   row.CustomerId,
			ClusterID:    row.ClusterId,
			DefinitionID: row.ReleaseDefinitionId,
			Namespace:    row.Namespace,
			ReleaseName:  row.ReleaseName,
			Revision:     row.Revision,
		}
		if active := row.ActiveOperation; active != nil {
			item.Active = &CleanupOperation{
				OperationID:   active.OperationId,
				OperationType: active.OperationType,
				State:         active.State.String(),
				Actor:         active.Actor,
			}
		}
		rows = append(rows, item)
	}
	return rows, nil
}

// CancelOperation implements Recovery. The Idempotency-Key header is required
// by the write contract (ADR-009; smoke.sh smoke-* convention).
func (r *LiveRecovery) CancelOperation(ctx context.Context, operationID, reason string) error {
	if strings.TrimSpace(operationID) == "" {
		return errors.New("cancel operation: empty operation id")
	}
	request := authorizedRequest(r.token(), &orchestratorv1.CancelOperationRequest{
		OperationId: operationID,
		Reason:      reason,
	})
	request.Header().Set("Idempotency-Key", r.idempotencyKey("cleanup-cancel", operationID))
	_, err := recoveryWrite(ctx, func(ctx context.Context) (string, error) {
		response, err := r.clients.orchestrator.CancelOperation(ctx, request)
		if err != nil {
			return "", err
		}
		return response.Msg.GetOperation().GetOperationId(), nil
	})
	return err
}

// RollbackRelease implements Recovery. The Idempotency-Key header is required
// by the write contract (ADR-009; smoke.sh smoke-* convention).
func (r *LiveRecovery) RollbackRelease(ctx context.Context, definitionID string, targetRevision, expectedCurrentRevision int32, reason string) error {
	if strings.TrimSpace(definitionID) == "" {
		return errors.New("rollback release: empty definition id")
	}
	request := authorizedRequest(r.token(), &orchestratorv1.RollbackReleaseRequest{
		ReleaseDefinitionId:     definitionID,
		TargetRevision:          targetRevision,
		ExpectedCurrentRevision: expectedCurrentRevision,
		Reason:                  reason,
	})
	request.Header().Set("Idempotency-Key", r.idempotencyKey("cleanup-rollback", definitionID))
	_, err := recoveryWrite(ctx, func(ctx context.Context) (string, error) {
		response, err := r.clients.orchestrator.RollbackRelease(ctx, request)
		if err != nil {
			return "", err
		}
		return response.Msg.GetOperationId(), nil
	})
	return err
}

// SetReplicas implements Recovery through the formal emergency-change API.
//
// The convergence policy matches the one the emergency stage uses for its own
// restore compensation (REVERT_ON_NEXT_RECONCILE): the write is an emergency
// change, so letting the next reconcile return the workload to its declared
// state is exactly the recovery semantics cleanup wants. The Idempotency-Key
// header and the message-level key are both required by the write contract
// (ADR-009).
//
// The restore is confirmed end to end, not just dispatched. Accepting the
// command and applying it are separate steps: the orchestrator returns as soon
// as it has written the command to the agent's stream, so a command accepted
// while that stream is reconnecting can be accepted and never applied. Cleanup
// then logged "restored baseline replicas" while the workload stayed at the count
// the run had set (real smoke 2026-09-11). Each attempt therefore awaits the
// operation's own terminal state, and an attempt that was accepted but never
// applied is re-issued under a fresh key — the key must change or the server
// dedupes the re-issue against the operation that just went nowhere.
//
// The Recovery interface takes a reason for symmetry with the other recovery
// writes; ExecuteEmergencyChangeRequest carries no reason field, so it is
// accepted and unused rather than silently appended to another field.
func (r *LiveRecovery) SetReplicas(ctx context.Context, definitionID, workloadRef string, replicas int32, _ string) error {
	if strings.TrimSpace(definitionID) == "" {
		return errors.New("set replicas: empty definition id")
	}
	if strings.TrimSpace(workloadRef) == "" {
		return errors.New("set replicas: empty workload ref")
	}
	if replicas < 0 {
		return errors.New("set replicas: negative replica count")
	}
	deadline := time.Now().Add(recoveryDeliveryRetryWindow)
	for attempt := 1; ; attempt++ {
		request := authorizedRequest(r.token(), &orchestratorv1.ExecuteEmergencyChangeRequest{
			ReleaseDefinitionId: definitionID,
			WorkloadRef:         workloadRef,
			ConvergenceStrategy: orchestratorv1.ConvergenceStrategy_REVERT_ON_NEXT_RECONCILE,
			SetReplicas:         replicas,
			IdempotencyKey:      r.emergencyAttemptKey(definitionID+"/"+workloadRef, attempt),
		})
		operationID, err := recoveryWrite(ctx, func(ctx context.Context) (string, error) {
			response, err := r.clients.orchestrator.ExecuteEmergencyChange(ctx, request)
			if err != nil {
				return "", err
			}
			return response.Msg.GetOperationId(), nil
		})
		if err == nil {
			err = r.awaitEmergencyApplied(ctx, operationID)
		}
		if err == nil {
			return nil
		}
		// A rejected write is rejected identically forever; only a delivery
		// failure or an operation that never reached success is worth re-issuing.
		if !retryableEmergencyFailure(err) || ctx.Err() != nil || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(recoveryDeliveryRetryInterval):
		}
	}
}

// emergencyAttemptKey scopes the emergency write to one delivery attempt. The
// first attempt keeps the invocation's key so a re-entered cleanup still dedupes
// against itself; later attempts must differ or the server replays the operation
// whose effect never landed.
func (r *LiveRecovery) emergencyAttemptKey(target string, attempt int) string {
	key := r.idempotencyKey("cleanup-set-replicas", target)
	if attempt <= 1 {
		return key
	}
	return key + "-attempt-" + strconv.Itoa(attempt)
}

// awaitEmergencyApplied polls the emergency operation until it reaches a
// successful terminal state. A terminal failure is returned as an error so the
// caller reports the workload as residual instead of as restored.
func (r *LiveRecovery) awaitEmergencyApplied(ctx context.Context, operationID string) error {
	if strings.TrimSpace(operationID) == "" {
		// Success without an operation identity cannot be confirmed, so it is a
		// failure rather than an assumption.
		return errors.New("emergency change: response carried no operation id")
	}
	ticker := time.NewTicker(emergencyApplyPollInterval)
	defer ticker.Stop()
	for {
		response, err := r.clients.orchestrator.GetOperation(ctx,
			authorizedRequest(r.token(), &orchestratorv1.GetOperationRequest{OperationId: operationID}))
		if err != nil {
			return err
		}
		operation := response.Msg.GetOperation()
		state := operation.GetState().String()
		if terminalState(state) {
			if operationSucceeded(state) {
				return nil
			}
			return fmt.Errorf("emergency change %s reached %s", operationID, state)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("emergency change %s still %s: %w", operationID, state, ctx.Err())
		case <-ticker.C:
		}
	}
}

// operationSucceeded reports whether an operation state is the success terminal
// state, using the same vocabulary as terminalState. The stage package would
// answer this directly, but it imports this one.
func operationSucceeded(state string) bool {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "OPERATION_STATUS_SUCCEEDED", "SUCCEEDED":
		return true
	default:
		return false
	}
}

// retryableEmergencyFailure reports whether a failed restore is worth another
// attempt. Delivery failures are retryable, and so is an operation that reached a
// non-success terminal state, because that is exactly the accepted-but-never-
// applied case this loop exists for. A rejected request is not: it fails the same
// way every time.
func retryableEmergencyFailure(err error) bool {
	if transientRecoveryError(err) {
		return true
	}
	// An operation that ended failed, cancelled, or timed out produced no error
	// code, so it is recognised by its own wording.
	return strings.HasPrefix(err.Error(), "emergency change ")
}

// emergencyApplyPollInterval is how often a confirmed restore re-reads the
// operation. The orchestrator's own emergency operation timeout is 30s, so a
// tighter poll only spends reads.
const emergencyApplyPollInterval = 2 * time.Second

// recoveryDeliveryRetryInterval is how long to wait before re-attempting a
// recovery write the orchestrator could not deliver.
const recoveryDeliveryRetryInterval = 5 * time.Second

// recoveryDeliveryRetryWindow bounds how long a recovery write waits for the
// operator agent to come back. The agent reconnects on its own backoff after the
// restart stage restarts the orchestrator and drops every command stream, and
// that backoff is measured in tens of seconds, so the window has to span it while
// staying far below an unbounded wait.
const recoveryDeliveryRetryWindow = 90 * time.Second

// recoveryWrite performs one recovery write, re-attempting while the failure is a
// delivery condition a later attempt can clear, and returns the operation id the
// response carried. Repeating is safe because every recovery write is idempotent
// under its key: the key and the body are fixed for the invocation, so the server
// dedupes a repeat rather than applying it twice.
//
// It exists because the prompt flow is `make e2e-all` immediately followed by
// `make e2e-cleanup`, and the run's own restart stage is what disconnects the
// agent the cleanup then needs (real smoke 2026-09-11: that pairing failed with
// "unavailable: emergency command delivery failed" and left the workload at the
// count the run had set).
func recoveryWrite(ctx context.Context, write func(context.Context) (string, error)) (string, error) {
	deadline := time.Now().Add(recoveryDeliveryRetryWindow)
	for {
		operationID, err := write(ctx)
		if err == nil {
			return operationID, nil
		}
		if !transientRecoveryError(err) || ctx.Err() != nil || !time.Now().Before(deadline) {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", err
		case <-time.After(recoveryDeliveryRetryInterval):
		}
	}
}

// transientRecoveryError reports whether a recovery write failed for a reason a
// later attempt can clear. Only delivery-level conditions qualify: a rejected
// write (invalid argument, permission denied, failed precondition) would be
// rejected identically forever and must surface on the first attempt.
//
// CodeUnknown is included because a transport-level failure that never reached
// the server is reported as Unknown rather than as a status code, which is the
// same reason the session waiter treats it as transient.
func transientRecoveryError(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeUnknown:
		return true
	default:
		return false
	}
}

// inventoryReadRetryInterval is how long to wait before re-reading the release
// inventory after a failure a later attempt can clear.
const inventoryReadRetryInterval = 5 * time.Second

// inventoryReadRetryWindow caps how long one inventory read keeps re-attempting,
// so a server failing for an unrelated reason cannot spend the whole cleanup
// budget here. The caller's deadline bounds it further.
const inventoryReadRetryWindow = 90 * time.Second

// retryInventoryRead re-attempts an inventory read while the failure is one a
// later attempt can clear, and returns the last failure once it cannot.
//
// CodeInternal is admitted here for a specific, documented reason rather than as
// a general habit: the orchestrator answers ListReleaseInventory with CodeInternal
// for every row whenever some definition has a non-terminal operation, because
// Postgres GetActiveForDefinition still selects the pre-emergency 16-column set
// while the shared scan reads 30 (D-033). That condition clears by itself -- an
// operation always reaches a terminal state -- and the read only observes, so
// repeating it cannot change what is being read.
func retryInventoryRead(
	ctx context.Context,
	read func(context.Context) (*orchestratorv1.ListReleaseInventoryResponse, error),
) (*orchestratorv1.ListReleaseInventoryResponse, error) {
	return retryInventoryReadEvery(ctx, inventoryReadRetryInterval, inventoryReadRetryWindow, read)
}

// retryInventoryReadEvery is retryInventoryRead with an explicit cadence, so a
// test can pin the retry contract without waiting out the production interval.
func retryInventoryReadEvery(
	ctx context.Context,
	interval, window time.Duration,
	read func(context.Context) (*orchestratorv1.ListReleaseInventoryResponse, error),
) (*orchestratorv1.ListReleaseInventoryResponse, error) {
	deadline := time.Now().Add(window)
	for {
		response, err := read(ctx)
		if err == nil {
			return response, nil
		}
		if !retryableInventoryReadFailure(err) || ctx.Err() != nil || !time.Now().Before(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(interval):
		}
	}
}

// retryableInventoryReadFailure reports whether a failed inventory read can
// succeed on a later attempt.
func retryableInventoryReadFailure(err error) bool {
	if transientRecoveryError(err) {
		return true
	}
	return connect.CodeOf(err) == connect.CodeInternal
}

// authorizedRequest builds a connect request carrying the runner bearer token
// (package-level helper: generic methods need go1.27, go.mod declares 1.26).
func authorizedRequest[T any](token string, message *T) *connect.Request[T] {
	request := connect.NewRequest(message)
	if token != "" {
		request.Header().Set("Authorization", "Bearer "+token)
	}
	return request
}

// idempotencyKey returns a stable-per-target write key. Using the target id
// (not a random suffix) makes cleanup exactly-once across replays while still
// being unique per operation/definition (ADR-009 scoped idempotency).
func idempotencyKey(kind, scope, target string) string {
	key := "e2e-cleanup-" + kind
	if scope != "" {
		key += "-" + scope
	}
	return key + "-" + target
}
