package orchestrator

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	operatorv1connect "github.com/ndzuki/release-manager/api/gen/operator/v1/operatorv1connect"
)

type connectEmergencyDispatcher struct {
	client operatorv1connect.OperatorServiceClient
}

func NewEmergencyDispatcher(client operatorv1connect.OperatorServiceClient) emergencyDispatcher {
	return &connectEmergencyDispatcher{client: client}
}

func (d *connectEmergencyDispatcher) DispatchEmergency(ctx context.Context, operatorID string, command *operatorv1.EmergencyCommand) error {
	if d == nil || d.client == nil {
		return fmt.Errorf("emergency dispatcher is unavailable")
	}
	_, err := d.client.DispatchEmergency(ctx, connect.NewRequest(&operatorv1.DispatchEmergencyRequest{
		OperatorId: operatorID,
		Command:    command,
	}))
	if err != nil {
		return fmt.Errorf("dispatch emergency command: %w", err)
	}
	return nil
}
