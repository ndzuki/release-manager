package operator

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// GetActiveOperatorSession returns the authoritative persisted active session
// for one operator without exposing its capability map.
func (s *Service) GetActiveOperatorSession(
	ctx context.Context,
	req *connect.Request[operatorv1.GetActiveOperatorSessionRequest],
) (*connect.Response[operatorv1.GetActiveOperatorSessionResponse], error) {
	operatorID := req.Msg.GetOperatorId()
	if operatorID == "" {
		return nil, operatorError(connect.CodeInvalidArgument, "operator_id_required", "operator_id is required")
	}

	session, err := s.store.Sessions().GetActiveByOperator(ctx, operatorID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, operatorError(connect.CodeNotFound, "session_not_found", "active operator session not found")
	}
	if err != nil {
		return nil, operatorError(connect.CodeInternal, reasonInternal, "active operator session lookup failed")
	}

	return connect.NewResponse(&operatorv1.GetActiveOperatorSessionResponse{
		Session: &operatorv1.OperatorSession{
			SessionId:           session.ID,
			OperatorId:          session.OperatorID,
			Status:              string(session.Status),
			InstanceId:          session.InstanceID,
			Version:             session.Version,
			ActiveConfigVersion: session.ActiveConfigVersion,
			StartedAt:           timestamppb.New(session.StartedAt),
			LastHeartbeat:       timestamppb.New(session.LastHeartbeat),
			ExpiresAt:           timestamppb.New(session.ExpiresAt),
		},
	}), nil
}
