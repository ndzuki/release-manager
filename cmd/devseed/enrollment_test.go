package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
)

// fakeTokenCreator implements the narrow enrollment seam against a scripted
// token response.
type fakeTokenCreator struct {
	createCalls int
	token       string
}

func (f *fakeTokenCreator) CreateEnrollmentToken(_ context.Context, _ *connect.Request[orchestratorv1.CreateEnrollmentTokenRequest]) (*connect.Response[orchestratorv1.CreateEnrollmentTokenResponse], error) {
	f.createCalls++
	return connect.NewResponse(&orchestratorv1.CreateEnrollmentTokenResponse{
		Token:     f.token,
		ExpiresAt: "2026-08-11T18:00:00+08:00",
	}), nil
}

func TestEnsureEnrollmentTokensWritesTokenFiles(t *testing.T) {
	dir := t.TempDir()
	client := &fakeTokenCreator{token: "deadbeefdeadbeefdeadbeefdeadbeef"}
	clusters := []clusterSeed{
		{id: "dev-customer-a-direct", customerID: "dev-customer-a"},
		{id: "dev-customer-b-replicated", customerID: "dev-customer-b"},
	}

	err := ensureEnrollmentTokens(context.Background(), client, clusters, dir)
	require.NoError(t, err)
	assert.Equal(t, 2, client.createCalls)

	for _, cluster := range clusters {
		data, err := os.ReadFile(filepath.Join(dir, cluster.id+".token"))
		require.NoError(t, err)
		assert.Contains(t, string(data), client.token)
		info, err := os.Stat(filepath.Join(dir, cluster.id+".token"))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func TestEnsureEnrollmentTokensIdempotent(t *testing.T) {
	dir := t.TempDir()
	client := &fakeTokenCreator{token: "token-v1"}
	clusters := []clusterSeed{{id: "dev-customer-a-direct", customerID: "dev-customer-a"}}

	// First run creates; second run must skip (file exists, no new RPC).
	require.NoError(t, ensureEnrollmentTokens(context.Background(), client, clusters, dir))
	require.NoError(t, ensureEnrollmentTokens(context.Background(), client, clusters, dir))
	assert.Equal(t, 1, client.createCalls)

	// The original token value is preserved across re-seeds.
	data, err := os.ReadFile(filepath.Join(dir, "dev-customer-a-direct.token"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "token-v1")
}
