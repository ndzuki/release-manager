package e2e

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
func (r *LiveRecovery) ListReleaseInventory(ctx context.Context) ([]CleanupRow, error) {
	response, err := r.clients.orchestrator.ListReleaseInventory(ctx, authorizedRequest(r.token(), &orchestratorv1.ListReleaseInventoryRequest{}))
	if err != nil {
		return nil, err
	}
	rows := make([]CleanupRow, 0, len(response.Msg.Rows))
	for _, row := range response.Msg.Rows {
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

// ReleaseDigest is one release's content identity.
//
// It exists because a revision number cannot answer "is this release back at the
// baseline". RollbackRelease advances the revision counter rather than restoring
// the number: a rollback to revision 21 was observed to read back as 23, and as
// 24 after the next one. Recovery decisions therefore compare the rendered values
// digest, which is the fact the rollback actually restores.
type ReleaseDigest struct {
	ReleaseDefinitionID string
	ValuesDigest        string
	Revision            int32
}

// ListReleaseDigests reads every release's content identity.
//
// ListReleaseInventory is the entry point because it answers without arguments
// and names the customers whose releases are in scope, while ListReleases needs a
// customer id per call. Reading the digests out of the inventory is not an
// option: the inventory deliberately does not expose them.
func (r *LiveRecovery) ListReleaseDigests(ctx context.Context) ([]ReleaseDigest, error) {
	inventory, err := r.clients.orchestrator.ListReleaseInventory(ctx,
		authorizedRequest(r.token(), &orchestratorv1.ListReleaseInventoryRequest{}))
	if err != nil {
		return nil, err
	}
	customers := make([]string, 0, len(inventory.Msg.Rows))
	seen := make(map[string]struct{}, len(inventory.Msg.Rows))
	for _, row := range inventory.Msg.Rows {
		if row == nil {
			continue
		}
		customer := strings.TrimSpace(row.GetCustomerId())
		if customer == "" {
			continue
		}
		if _, ok := seen[customer]; ok {
			continue
		}
		seen[customer] = struct{}{}
		customers = append(customers, customer)
	}
	sort.Strings(customers)

	digests := make([]ReleaseDigest, 0, len(inventory.Msg.Rows))
	for _, customer := range customers {
		cursor := ""
		for {
			response, err := r.clients.orchestrator.ListReleases(ctx,
				authorizedRequest(r.token(), &orchestratorv1.ListReleasesRequest{
					CustomerId: customer,
					Cursor:     cursor,
				}))
			if err != nil {
				return nil, err
			}
			for _, release := range response.Msg.GetReleases() {
				if release == nil || strings.TrimSpace(release.GetReleaseDefinitionId()) == "" {
					continue
				}
				digests = append(digests, ReleaseDigest{
					ReleaseDefinitionID: release.GetReleaseDefinitionId(),
					ValuesDigest:        release.GetValuesDigest(),
					Revision:            release.GetRevision(),
				})
			}
			// A server that repeats a cursor would loop forever; treating a
			// repeated cursor as the end keeps the read bounded.
			next := strings.TrimSpace(response.Msg.GetNextCursor())
			if next == "" || next == cursor {
				break
			}
			cursor = next
		}
	}
	sort.SliceStable(digests, func(i, j int) bool {
		return digests[i].ReleaseDefinitionID < digests[j].ReleaseDefinitionID
	})
	return digests, nil
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
	created, err := recoveryWrite(ctx, func(ctx context.Context) (string, error) {
		response, err := r.clients.orchestrator.CancelOperation(ctx, request)
		if err != nil {
			return "", err
		}
		return response.Msg.GetOperation().GetOperationId(), nil
	})
	if err != nil {
		return err
	}
	return r.confirmRecoveryOperation(ctx, created)
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
	created, err := recoveryWrite(ctx, func(ctx context.Context) (string, error) {
		response, err := r.clients.orchestrator.RollbackRelease(ctx, request)
		if err != nil {
			return "", err
		}
		return response.Msg.GetOperationId(), nil
	})
	if err != nil {
		return err
	}
	return r.confirmRecoveryOperation(ctx, created)
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
	deadline := time.Now().Add(recoveryRetryWindow(ctx))
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
			err = r.confirmRecoveryOperation(ctx, operationID)
		}
		if err == nil {
			return nil
		}
		if !retryableRecoveryFailure(err) || ctx.Err() != nil || !time.Now().Before(deadline) {
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

// confirmRecoveryOperation polls an operation until it reaches a terminal state
// and succeeds only on the success state.
//
// Every recovery write needs this, not just the emergency one: the orchestrator
// returns once it has dispatched the command, so an RPC that returned nil says
// the work was requested and nothing about whether it happened. Without this a
// cancel or rollback that never completed was still counted as recovered
// (real smoke 2026-09-11, where a replica restore was reported while the
// workload kept the count the run had set).
//
// It lives in the implementation rather than the Recovery interface on purpose:
// the interface keeps its shape, and no call site can opt out of confirming.
func (r *LiveRecovery) confirmRecoveryOperation(ctx context.Context, operationID string) error {
	if strings.TrimSpace(operationID) == "" {
		// Success without an operation identity cannot be confirmed, so it is a
		// failure rather than an assumption.
		return errors.New("recovery operation: response carried no operation id")
	}
	ticker := time.NewTicker(recoveryOperationPollInterval)
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
			return &recoveryOperationError{
				operationID: operationID,
				state:       state,
				lastError:   operation.GetLastError(),
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("recovery operation %s still %s: %w", operationID, state, ctx.Err())
		case <-ticker.C:
		}
	}
}

// recoveryOperationError reports an operation that reached a terminal state other
// than success.
type recoveryOperationError struct {
	operationID string
	state       string
	lastError   string
}

func (e *recoveryOperationError) Error() string {
	if e.lastError != "" {
		return fmt.Sprintf("recovery operation %s reached %s: %s", e.operationID, e.state, e.lastError)
	}
	return fmt.Sprintf("recovery operation %s reached %s", e.operationID, e.state)
}

// targetLocked reports whether the operation failed because the emergency target
// lock is held by an effect that is still unresolved.
//
// The distinction is actionable, not cosmetic: the lock is not released on a TTL
// while an effect is unresolved, so re-issuing the write cannot clear it and only
// burns the recovery budget. The caller must surface it as its own condition.
// The signal is the reason-code name in the operation's last error, because
// Operation carries no typed reason-code field.
func (e *recoveryOperationError) targetLocked() bool {
	return strings.Contains(e.lastError, "LOCKED_PATH")
}

// retryableRecoveryFailure reports whether a failed recovery write is worth
// another attempt. Delivery failures are retryable, and so is an operation that
// reached a non-success terminal state, because that is the accepted-but-never-
// applied case the re-issue exists for. A rejected request is not (it fails the
// same way every time), and neither is a held target lock (it cannot clear).
func retryableRecoveryFailure(err error) bool {
	var locked *recoveryOperationError
	if errors.As(err, &locked) {
		return !locked.targetLocked()
	}
	return transientRecoveryError(err)
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

// recoveryOperationPollInterval is how often a confirmed recovery write re-reads
// its operation. The orchestrator's own emergency operation timeout is 30s, so a
// tighter poll only spends reads.
const recoveryOperationPollInterval = 2 * time.Second

// recoveryDeliveryRetryInterval is how long to wait before re-attempting a
// recovery write the orchestrator could not deliver.
const recoveryDeliveryRetryInterval = 5 * time.Second

// recoveryDeliveryRetryCap is the most one recovery write may spend re-attempting
// delivery or re-issuing an operation that never succeeded, when the caller's
// context allows more.
//
// It is a cap rather than the window itself: the window is derived from the
// caller's deadline (recoveryRetryWindow) so a command budget and a retry window
// cannot drift apart. Deriving it matters because a budget shorter than the window
// silently caps every restore — which is exactly how the 30s default failed
// (D-032).
const recoveryDeliveryRetryCap = 90 * time.Second

// recoveryRetryWindow returns how long a recovery write may spend retrying: the
// smaller of recoveryDeliveryRetryCap and whatever the caller's context leaves.
// A caller with no deadline gets the cap, so the window is always bounded.
func recoveryRetryWindow(ctx context.Context) time.Duration {
	window := recoveryDeliveryRetryCap
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < window {
			window = remaining
		}
	}
	if window < 0 {
		return 0
	}
	return window
}

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
	deadline := time.Now().Add(recoveryRetryWindow(ctx))
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
