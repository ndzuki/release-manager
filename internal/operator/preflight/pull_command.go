package preflight

import (
	"context"
	"fmt"
	"time"
)

type RuntimePullConfig struct {
	Enabled        bool
	Namespace      string
	ServiceAccount string
	Timeout        time.Duration
	CleanupPolicy  CleanupPolicy
	ProbeCommand   []string
}

type RuntimePullExecutor struct {
	config RuntimePullConfig
	prober *PullProber
}

func NewRuntimePullExecutor(prober *PullProber, config RuntimePullConfig) *RuntimePullExecutor {
	if config.CleanupPolicy == "" {
		config.CleanupPolicy = CleanupAlways
	}
	return &RuntimePullExecutor{config: config, prober: prober}
}

func (e *RuntimePullExecutor) Enabled() bool {
	return e != nil && e.config.Enabled
}

func (e *RuntimePullExecutor) Run(ctx context.Context, operationID string, images []string) (*PullBatchResult, error) {
	if e == nil || !e.config.Enabled {
		return nil, ErrPullDisabled
	}
	if e.prober == nil {
		return nil, fmt.Errorf("%w: pull prober is required", ErrPullInputInvalid)
	}
	return e.prober.Probe(ctx, PullInput{
		OperationID:    operationID,
		Namespace:      e.config.Namespace,
		ServiceAccount: e.config.ServiceAccount,
		Images:         images,
		Timeout:        e.config.Timeout,
		CleanupPolicy:  e.config.CleanupPolicy,
		ProbeCommand:   append([]string(nil), e.config.ProbeCommand...),
	})
}

func (e *RuntimePullExecutor) AllowsExecution(result *PullBatchResult) bool {
	if e == nil || !e.config.Enabled {
		return true
	}
	return PullGate(result)
}
