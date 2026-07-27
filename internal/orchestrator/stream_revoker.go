package orchestrator

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
)

// OperatorStreamRevoker closes active Operator command streams after Store commit.
type OperatorStreamRevoker interface {
	Revoke(context.Context, string, string) error
}

type connectOperatorStreamRevoker struct {
	client operatorv1connect.OperatorServiceClient
}

// NewConnectOperatorStreamRevoker creates the control-plane client used to
// close active Operator streams after a committed revocation.
func NewConnectOperatorStreamRevoker(httpClient connect.HTTPClient, baseURL string) OperatorStreamRevoker {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &connectOperatorStreamRevoker{
		client: operatorv1connect.NewOperatorServiceClient(httpClient, strings.TrimRight(baseURL, "/")),
	}
}

func (r *connectOperatorStreamRevoker) Revoke(ctx context.Context, operatorID, reason string) error {
	if r == nil || r.client == nil || operatorID == "" {
		return errors.New("revoke operator stream: revoker and operator_id are required")
	}
	_, err := r.client.RevokeOperator(ctx, connect.NewRequest(&operatorv1.RevokeOperatorRequest{
		OperatorId: operatorID,
		Reason:     reason,
	}))
	if err != nil {
		return err
	}
	return nil
}
