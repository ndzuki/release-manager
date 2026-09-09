package e2e

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"connectrpc.com/connect"
	authv1 "github.com/ndzuki/release-manager/api/gen/auth/v1"
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
)

// ErrRunnerLogin reports an e2e-runner authentication failure.
var ErrRunnerLogin = errors.New("e2e-runner login failed")

// LiveRecovery implements Recovery over the formal Connect surface using the
// e2e-runner development account (REQ-065). It performs no kubectl/helm/db
// access; generated clients and the access token remain private to the struct.
type LiveRecovery struct {
	cfg     *Config
	clients *ClientBundle

	mu          sync.Mutex
	accessToken string
	userID      string
}

// NewLiveRecovery constructs the formal-API recovery implementation. The
// config must already be validated (LoadConfig resolves the password).
func NewLiveRecovery(cfg *Config, clients *ClientBundle) (*LiveRecovery, error) {
	if cfg == nil {
		return nil, configInvalid("config", "nil config")
	}
	if clients == nil {
		return nil, configInvalid("clients", "nil client bundle")
	}
	return &LiveRecovery{cfg: cfg, clients: clients}, nil
}

// Login authenticates as e2e-runner and resolves the authoritative user id.
// The user id is read from ValidateToken because LoginResponse.user may be
// unset on the current auth version (see smoke.sh D-029 D4).
func (r *LiveRecovery) Login(ctx context.Context) (string, error) {
	if r == nil || r.cfg == nil || r.clients == nil {
		return "", ErrRunnerLogin
	}
	password := r.cfg.Password()
	if password == "" {
		return "", fmt.Errorf("%w: password environment variable is not set", ErrRunnerLogin)
	}
	login, err := r.clients.auth.Login(ctx, connect.NewRequest(&authv1.LoginRequest{
		Username: r.cfg.Credentials.E2ERunner.Username,
		Password: password,
	}))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrRunnerLogin, err)
	}
	if login == nil || login.Msg == nil {
		return "", fmt.Errorf("%w: empty login response", ErrRunnerLogin)
	}
	accessToken := login.Msg.AccessToken
	if accessToken == "" {
		return "", fmt.Errorf("%w: empty access token", ErrRunnerLogin)
	}

	userID := ""
	if login.Msg.User != nil && login.Msg.User.Id != "" {
		userID = login.Msg.User.Id
	} else {
		valid, err := r.clients.auth.ValidateToken(ctx, connect.NewRequest(&authv1.ValidateTokenRequest{Token: accessToken}))
		if err != nil {
			return "", fmt.Errorf("%w: validate token: %v", ErrRunnerLogin, err)
		}
		if valid.Msg == nil || !valid.Msg.Valid {
			return "", fmt.Errorf("%w: token validation rejected", ErrRunnerLogin)
		}
		userID = valid.Msg.UserId
	}

	r.mu.Lock()
	r.accessToken = accessToken
	r.userID = userID
	r.mu.Unlock()
	return userID, nil
}

// UserID returns the authenticated runner user id after a successful Login.
func (r *LiveRecovery) UserID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.userID
}

// token returns the current bearer token (empty before Login).
func (r *LiveRecovery) token() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.accessToken
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
	request.Header().Set("Idempotency-Key", idempotencyKey("cleanup-cancel", operationID))
	_, err := r.clients.orchestrator.CancelOperation(ctx, request)
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
	request.Header().Set("Idempotency-Key", idempotencyKey("cleanup-rollback", definitionID))
	_, err := r.clients.orchestrator.RollbackRelease(ctx, request)
	return err
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
func idempotencyKey(kind, target string) string {
	return "e2e-cleanup-" + kind + "-" + target
}
