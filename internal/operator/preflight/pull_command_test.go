package preflight

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

func TestRuntimePullExecutorDisabledDoesNotCreateResources(t *testing.T) {
	executor := NewRuntimePullExecutor(NewPullProber(kubernetesfake.NewSimpleClientset(), nil), RuntimePullConfig{})
	result, err := executor.Run(context.Background(), "operation-1", []string{testImageOne})
	require.ErrorIs(t, err, ErrPullDisabled)
	assert.Nil(t, result)
	assert.True(t, executor.AllowsExecution(nil))
}

func TestRuntimePullExecutorRequiresPassedResult(t *testing.T) {
	executor := NewRuntimePullExecutor(nil, RuntimePullConfig{Enabled: true})
	assert.False(t, executor.AllowsExecution(&PullBatchResult{Passed: false}))
	assert.True(t, executor.AllowsExecution(&PullBatchResult{Passed: true}))
}
