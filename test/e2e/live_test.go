package e2e

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	orchestratorv1connect "github.com/ndzuki/release-manager/api/gen/orchestrator/v1/orchestratorv1connect"
	"google.golang.org/protobuf/proto"
)

// TestTransientRecoveryErrorClassifiesDeliveryConditions pins which failures a
// later attempt can clear. Getting this wrong in either direction is costly:
// treating a rejection as transient hides a permanent failure behind a 90s
// wait, and treating a delivery failure as permanent means cleanup reports
// residual for a workload it could have restored once the operator agent
// reconnected.
func TestTransientRecoveryErrorClassifiesDeliveryConditions(t *testing.T) {
	t.Parallel()

	transient := []error{
		connect.NewError(connect.CodeUnavailable, errors.New("emergency command delivery failed")),
		connect.NewError(connect.CodeDeadlineExceeded, errors.New("timeout")),
		// A transport-level failure that never reached the server is Unknown.
		errors.New("write envelope: EOF"),
	}
	for _, err := range transient {
		if !transientRecoveryError(err) {
			t.Fatalf("transientRecoveryError(%v) = false, want true", err)
		}
	}
	permanent := []error{
		connect.NewError(connect.CodeInvalidArgument, errors.New("bad workload ref")),
		connect.NewError(connect.CodePermissionDenied, errors.New("denied")),
		connect.NewError(connect.CodeFailedPrecondition, errors.New("wrong revision")),
		connect.NewError(connect.CodeAlreadyExists, errors.New("idempotency_conflict")),
	}
	for _, err := range permanent {
		if transientRecoveryError(err) {
			t.Fatalf("transientRecoveryError(%v) = true, want false", err)
		}
	}
}

// TestLiveRecoverySetReplicasRetriesWhileTheOperatorReconnects covers the wiring
// rather than the helper: exercising recoveryWrite directly proved only that the
// retry works, not that the recovery write uses it, and SetReplicas was the one
// write left unwrapped. Cleanup run immediately after e2e-all then failed in
// three seconds with "emergency command delivery failed" while the operator agent
// was still reconnecting from the run's own restart stage.
func TestLiveRecoverySetReplicasRetriesWhileTheOperatorReconnects(t *testing.T) {
	t.Parallel()

	var deliveries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "ExecuteEmergencyChange"):
			if deliveries.Add(1) == 1 {
				// What the orchestrator answers while the agent's command stream
				// is reconnecting.
				writeConnectError(t, writer, http.StatusServiceUnavailable, "unavailable", "emergency command delivery failed")
				return
			}
			writeProto(t, writer, &orchestratorv1.ExecuteEmergencyChangeResponse{
				OperationId: "op-1",
				Result:      &orchestratorv1.EmergencyResult{Requested: true},
			})
		case strings.HasSuffix(request.URL.Path, "GetOperation"):
			writeProto(t, writer, &orchestratorv1.GetOperationResponse{
				Operation: &orchestratorv1.Operation{
					OperationId: "op-1",
					State:       orchestratorv1.OperationStatus_OPERATION_STATUS_SUCCEEDED,
				},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	recovery := &LiveRecovery{
		clients: &ClientBundle{
			orchestrator: orchestratorv1connect.NewOrchestratorServiceClient(server.Client(), server.URL),
		},
		scope: "test-scope",
	}
	if err := recovery.SetReplicas(context.Background(), "def-1", "deployments/ns/app", 1, "restore"); err != nil {
		t.Fatalf("SetReplicas() error = %v, want the retry to land the restore", err)
	}
	if got := deliveries.Load(); got != 2 {
		t.Fatalf("deliveries = %d, want 2 (SetReplicas must go through the delivery retry)", got)
	}
}

// TestLiveRecoverySetReplicasReissuesAnOperationThatNeverApplies covers the
// second failure mode behind the same symptom. The orchestrator accepts the
// command as soon as it writes it to the agent's stream, so a command accepted
// during a reconnect can be accepted and never applied: cleanup then logged
// "restored baseline replicas" while the workload stayed at the count the run had
// set. The restore must follow the operation to its terminal state and re-issue
// under a fresh key when it never succeeds.
func TestLiveRecoverySetReplicasReissuesAnOperationThatNeverApplies(t *testing.T) {
	t.Parallel()

	var deliveries atomic.Int32
	var keys []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "ExecuteEmergencyChange"):
			// The key differs per attempt, so a re-issue is not deduped into the
			// operation that went nowhere.
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Errorf("read request body: %v", err)
				return
			}
			var message orchestratorv1.ExecuteEmergencyChangeRequest
			if err := proto.Unmarshal(body, &message); err != nil {
				t.Errorf("unmarshal request: %v", err)
				return
			}
			mu.Lock()
			keys = append(keys, message.GetIdempotencyKey())
			mu.Unlock()
			writeProto(t, writer, &orchestratorv1.ExecuteEmergencyChangeResponse{
				OperationId: "op-" + strconv.Itoa(int(deliveries.Add(1))),
			})
		case strings.HasSuffix(request.URL.Path, "GetOperation"):
			// The first operation never applies; the second succeeds.
			state := orchestratorv1.OperationStatus_OPERATION_STATUS_TIMEOUT
			if deliveries.Load() > 1 {
				state = orchestratorv1.OperationStatus_OPERATION_STATUS_SUCCEEDED
			}
			writeProto(t, writer, &orchestratorv1.GetOperationResponse{
				Operation: &orchestratorv1.Operation{State: state},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	recovery := &LiveRecovery{
		clients: &ClientBundle{
			orchestrator: orchestratorv1connect.NewOrchestratorServiceClient(server.Client(), server.URL),
		},
		scope: "test-scope",
	}
	if err := recovery.SetReplicas(context.Background(), "def-1", "deployments/ns/app", 1, "restore"); err != nil {
		t.Fatalf("SetReplicas() error = %v, want the re-issue to land the restore", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 2 {
		t.Fatalf("deliveries = %d, want 2 (the dropped operation must be re-issued)", len(keys))
	}
	if keys[0] == keys[1] {
		t.Fatalf("both attempts used key %q, want a fresh key or the server replays the dropped operation", keys[0])
	}
}

// TestLiveRecoverySetReplicasFailsWhenTheOperationNeverSucceeds proves the
// restore reports failure instead of claiming a restore that never converged:
// cleanup must record the workload as residual.
func TestLiveRecoverySetReplicasFailsWhenTheOperationNeverSucceeds(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "ExecuteEmergencyChange"):
			writeProto(t, writer, &orchestratorv1.ExecuteEmergencyChangeResponse{OperationId: "op-1"})
		case strings.HasSuffix(request.URL.Path, "GetOperation"):
			writeProto(t, writer, &orchestratorv1.GetOperationResponse{
				Operation: &orchestratorv1.Operation{State: orchestratorv1.OperationStatus_OPERATION_STATUS_TIMEOUT},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	recovery := &LiveRecovery{
		clients: &ClientBundle{
			orchestrator: orchestratorv1connect.NewOrchestratorServiceClient(server.Client(), server.URL),
		},
		scope: "test-scope",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := recovery.SetReplicas(ctx, "def-1", "deployments/ns/app", 1, "restore"); err == nil {
		t.Fatal("SetReplicas() error = nil, want a restore that never applied reported as a failure")
	}
}

// TestLiveRecoveryCancelOperationConfirmsTheOperation covers the uniform rule:
// confirming the terminal state lives in the implementation, so cancel and
// rollback inherit it and no call site can opt out. Before this, only the replica
// restore confirmed anything, and a cancel that never completed was still counted
// as recovered.
func TestLiveRecoveryCancelOperationConfirmsTheOperation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "CancelOperation"):
			writeProto(t, writer, &orchestratorv1.CancelOperationResponse{
				Operation: &orchestratorv1.Operation{OperationId: "op-cancel"},
			})
		case strings.HasSuffix(request.URL.Path, "GetOperation"):
			writeProto(t, writer, &orchestratorv1.GetOperationResponse{
				Operation: &orchestratorv1.Operation{
					OperationId: "op-cancel",
					State:       orchestratorv1.OperationStatus_OPERATION_STATUS_FAILED,
					LastError:   "cancel rejected",
				},
			})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	recovery := &LiveRecovery{
		clients: &ClientBundle{
			orchestrator: orchestratorv1connect.NewOrchestratorServiceClient(server.Client(), server.URL),
		},
		scope: "test-scope",
	}
	err := recovery.CancelOperation(context.Background(), "op-cancel", "cleanup")
	if err == nil {
		t.Fatal("CancelOperation() error = nil, want a cancel that never completed reported as a failure")
	}
	var refused *recoveryOperationError
	if !errors.As(err, &refused) {
		t.Fatalf("CancelOperation() error = %v, want the terminal state surfaced", err)
	}
	if refused.lastError != "cancel rejected" {
		t.Fatalf("lastError = %q, want the operation's own last error carried through", refused.lastError)
	}
}

// TestRecoveryRetryWindowIsBoundedByTheCallerContext pins the derivation that
// keeps a retry window and a command budget from drifting apart. A budget shorter
// than the window silently caps every restore, which is how the 30s default
// failed (D-032).
func TestRecoveryRetryWindowIsBoundedByTheCallerContext(t *testing.T) {
	t.Parallel()

	if got := recoveryRetryWindow(context.Background()); got != recoveryDeliveryRetryCap {
		t.Fatalf("recoveryRetryWindow(no deadline) = %s, want the cap %s", got, recoveryDeliveryRetryCap)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got := recoveryRetryWindow(ctx)
	if got <= 0 || got > 3*time.Second {
		t.Fatalf("recoveryRetryWindow(3s budget) = %s, want it clamped to the budget", got)
	}

	expired, cancelExpired := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelExpired()
	<-expired.Done()
	if got := recoveryRetryWindow(expired); got != 0 {
		t.Fatalf("recoveryRetryWindow(expired) = %s, want 0", got)
	}
}

// TestRetryableRecoveryFailureRecognisesAHeldTargetLock pins the one terminal
// failure that must not be retried. The emergency target lock is not released on
// a TTL while an effect is unresolved, so re-issuing cannot clear it and only
// burns the recovery budget.
func TestRetryableRecoveryFailureRecognisesAHeldTargetLock(t *testing.T) {
	t.Parallel()

	locked := &recoveryOperationError{
		operationID: "op-1",
		state:       "OPERATION_STATUS_FAILED",
		lastError:   "EMERGENCY_REASON_CODE_LOCKED_PATH",
	}
	if retryableRecoveryFailure(locked) {
		t.Fatal("retryableRecoveryFailure(held lock) = true, want false: re-issuing cannot clear a held lock")
	}
	if !locked.targetLocked() {
		t.Fatal("targetLocked() = false, want the lock recognised from the reason code")
	}

	other := &recoveryOperationError{operationID: "op-2", state: "OPERATION_STATUS_TIMEOUT"}
	if !retryableRecoveryFailure(other) {
		t.Fatal("retryableRecoveryFailure(timeout) = false, want the never-applied case retried")
	}
	if other.targetLocked() {
		t.Fatal("targetLocked() = true for an unrelated failure")
	}

	if retryableRecoveryFailure(connect.NewError(connect.CodePermissionDenied, errors.New("denied"))) {
		t.Fatal("retryableRecoveryFailure(rejected) = true, want a rejection surfaced immediately")
	}
}

// writeProto answers a Connect call with a proto-encoded message, the codec the
// clients use.
func writeProto(t *testing.T, writer http.ResponseWriter, message proto.Message) {
	t.Helper()
	body, err := proto.Marshal(message)
	if err != nil {
		t.Errorf("marshal %T: %v", message, err)
		return
	}
	writer.Header().Set("Content-Type", "application/proto")
	writer.WriteHeader(http.StatusOK)
	if _, err := writer.Write(body); err != nil {
		t.Errorf("write %T: %v", message, err)
	}
}

// writeConnectError answers a Connect call with a status the client maps back to
// a connect code.
func writeConnectError(t *testing.T, writer http.ResponseWriter, status int, code, message string) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if _, err := fmt.Fprintf(writer, `{"code":%q,"message":%q}`, code, message); err != nil {
		t.Errorf("write connect error: %v", err)
	}
}

// TestRecoveryWriteRetriesATransientDeliveryFailure proves a write that fails
// while the operator agent is reconnecting still lands, and that the retry is
// bounded by the caller's context rather than spinning.
func TestRecoveryWriteRetriesATransientDeliveryFailure(t *testing.T) {
	t.Parallel()

	attempts := 0
	operationID, err := recoveryWrite(context.Background(), func(context.Context) (string, error) {
		attempts++
		if attempts == 1 {
			return "", connect.NewError(connect.CodeUnavailable, errors.New("emergency command delivery failed"))
		}
		return "op-1", nil
	})
	if err != nil {
		t.Fatalf("recoveryWrite() error = %v, want the retry to succeed", err)
	}
	if operationID != "op-1" {
		t.Fatalf("operationID = %q, want the response's operation id surfaced", operationID)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want exactly 2 (one failure, one success)", attempts)
	}
}

// TestRecoveryWriteDoesNotRetryARejectedWrite keeps a permanent rejection on the
// first attempt: retrying it would only delay an honest failure.
func TestRecoveryWriteDoesNotRetryARejectedWrite(t *testing.T) {
	t.Parallel()

	attempts := 0
	_, err := recoveryWrite(context.Background(), func(context.Context) (string, error) {
		attempts++
		return "", connect.NewError(connect.CodePermissionDenied, errors.New("denied"))
	})
	if err == nil {
		t.Fatal("recoveryWrite() error = nil, want the rejection surfaced")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry for a rejected write)", attempts)
	}
}

// TestRecoveryWriteStopsWhenTheContextIsDone proves the retry honours a caller
// deadline instead of waiting out its own window.
func TestRecoveryWriteStopsWhenTheContextIsDone(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	attempts := 0
	_, err := recoveryWrite(ctx, func(context.Context) (string, error) {
		attempts++
		return "", connect.NewError(connect.CodeUnavailable, errors.New("still reconnecting"))
	})
	if err == nil {
		t.Fatal("recoveryWrite() error = nil, want the delivery failure surfaced")
	}
	if attempts > 2 {
		t.Fatalf("attempts = %d, want the wait bounded by the context", attempts)
	}
}
