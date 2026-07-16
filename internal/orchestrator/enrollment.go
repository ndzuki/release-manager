package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// CreateEnrollmentToken generates a single-use enrollment token for operator registration.
func (s *Service) CreateEnrollmentToken(
	ctx context.Context,
	req *connect.Request[orchestratorv1.CreateEnrollmentTokenRequest],
) (*connect.Response[orchestratorv1.CreateEnrollmentTokenResponse], error) {
	msg := req.Msg

	// Verify customer exists and is active.
	cust, err := s.store.Customers().Get(ctx, msg.GetCustomerId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("customer %q not found", msg.GetCustomerId()))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if cust.Status == store.CustomerDisabled {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("customer %q is disabled", msg.GetCustomerId()))
	}

	// Verify cluster exists and is active.
	cl, err := s.store.Clusters().Get(ctx, msg.GetClusterId())
	if err != nil {
		if err == store.ErrNotFound {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("cluster %q not found", msg.GetClusterId()))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if cl.Status == store.ClusterDisabled {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("cluster %q is disabled", msg.GetClusterId()))
	}
	if cl.CustomerID != msg.GetCustomerId() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("cluster %q does not belong to customer %q", msg.GetClusterId(), msg.GetCustomerId()))
	}

	// Generate a cryptographically random token.
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("generate token: %w", err))
	}
	token := hex.EncodeToString(tokenBytes)

	now := time.Now().UTC()
	t := &store.EnrollmentToken{
		ID:         uuid.New().String(),
		CustomerID: msg.GetCustomerId(),
		ClusterID:  msg.GetClusterId(),
		Token:      token,
		CreatedAt:  now,
		ExpiresAt:  now.Add(1 * time.Hour),
	}

	if err := s.store.EnrollmentTokens().Create(ctx, t); err != nil {
		s.logger.Error("create enrollment token failed", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create enrollment token: %w", err))
	}

	s.logger.Info("enrollment token created", "customer_id", t.CustomerID, "cluster_id", t.ClusterID)
	return connect.NewResponse(&orchestratorv1.CreateEnrollmentTokenResponse{
		Token:     t.Token,
		ExpiresAt: t.ExpiresAt.Format(time.RFC3339),
	}), nil
}
