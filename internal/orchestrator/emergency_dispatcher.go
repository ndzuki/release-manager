package orchestrator

import (
	"context"
	"fmt"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
)

type EmergencyDispatchClient interface {
	DispatchEmergency(context.Context, string, *operatorv1.EmergencyCommand) error
}

type rpcEmergencyDispatcher struct {
	client EmergencyDispatchClient
}

func NewEmergencyDispatcher(client EmergencyDispatchClient) emergencyDispatcher {
	return &rpcEmergencyDispatcher{client: client}
}

func (d *rpcEmergencyDispatcher) DispatchEmergency(ctx context.Context, operatorID string, command *operatorv1.EmergencyCommand) error {
	if d == nil || d.client == nil {
		return fmt.Errorf("emergency dispatcher is unavailable")
	}
	if err := d.client.DispatchEmergency(ctx, operatorID, command); err != nil {
		return fmt.Errorf("dispatch emergency command: %w", err)
	}
	return nil
}
