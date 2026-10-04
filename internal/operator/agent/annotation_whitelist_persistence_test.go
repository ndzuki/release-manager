package agent

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/ndzuki/release-manager/internal/operator/localstore"
)

// annotationAgentWithStore is annotationAgentWithEngine with a caller-supplied
// store, so a restart test can hand the second Agent the same on-disk state.
func annotationAgentWithStore(t *testing.T, engine helmengine.Engine, store localstore.Store) *Agent {
	t.Helper()
	agent, err := New(Config{
		Client: noopClient{}, Engine: engine, Store: store,
		SessionID: "session-1", OperatorID: "operator-1",
		KubeClient:   kubernetesfake.NewSimpleClientset(annotationDeployment()),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		InstallFlags: InstallFlags{Atomic: true, Timeout: time.Minute},
	})
	require.NoError(t, err)
	return agent
}

// openBoltAnnotationStore opens a real BoltDB-backed store on path, the way the
// operator process does (cmd/operator uses localstore.OpenBolt).
func openBoltAnnotationStore(t *testing.T, path string) *localstore.BoltStore {
	t.Helper()
	store, err := localstore.OpenBolt(path)
	require.NoError(t, err)
	return store
}

// TASK-244 core case: agent A learns a release's annotation whitelist from a
// release write; the process then restarts (the BoltDB file handle closes and
// the in-memory cache is gone) and agent B reopens the same store. B must
// report the same annotations with NO new command, otherwise annotation
// observation silently degrades until the next release write.
func TestAgent_AnnotationWhitelistSurvivesRestartWithoutNewCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator.db")

	storeA := openBoltAnnotationStore(t, path)
	agentA := annotationAgentWithStore(t, &recordingEngine{release: annotationRelease()}, storeA)
	streamA := newTestStream()
	command := installCommand("cmd-restart-whitelist")
	command.ApprovedAnnotationKeys = annotationWhitelist()
	require.NoError(t, agentA.handleCommand(t.Context(), streamA, command))
	require.Len(t, workloadIdentityReports(streamA), 1)
	require.NotEmpty(t, agentA.approvedAnnotationsFor("apps", "example"),
		"precondition: agent A cached the whitelist")

	// The operator process exits: the file handle and its lock are released and
	// the in-memory cache dies with agent A.
	require.NoError(t, storeA.Close())

	storeB := openBoltAnnotationStore(t, path)
	t.Cleanup(func() { _ = storeB.Close() })
	agentB := annotationAgentWithStore(t, &recordingEngine{release: annotationRelease()}, storeB)
	require.NotEmpty(t, agentB.approvedAnnotationsFor("apps", "example"),
		"a restarted operator must hydrate the whitelist from disk")

	items := agentB.buildWorkloadIdentityItems(t.Context(), annotationRelease())
	require.Len(t, items, 1)
	assert.Equal(t, map[string]map[string]string{
		"WORKLOAD_METADATA":     {"team": "platform", "tier": "web"},
		"POD_TEMPLATE_METADATA": {"prometheus.io/scrape": "true"},
	}, flattenScopedAnnotations(items[0].GetCurrentAnnotations()),
		"the restarted operator must report the same annotations with no new command")
}

// TASK-244 negative control: a release write that carries no whitelist clears
// the persisted copy too, so the restart must NOT revive the old whitelist.
func TestAgent_AnnotationWhitelistRestartDoesNotReviveClearedWhitelist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator.db")

	storeA := openBoltAnnotationStore(t, path)
	agentA := annotationAgentWithStore(t, &recordingEngine{release: annotationRelease()}, storeA)

	primed := installCommand("cmd-restart-clear-1")
	primed.ApprovedAnnotationKeys = annotationWhitelist()
	require.NoError(t, agentA.handleCommand(t.Context(), newTestStream(), primed))
	require.NotEmpty(t, agentA.approvedAnnotationsFor("apps", "example"))

	// The center removed every approved key: the release write carries none and
	// must clear both the memory cache and the persisted copy.
	cleared := installCommand("cmd-restart-clear-2")
	require.NoError(t, agentA.handleCommand(t.Context(), newTestStream(), cleared))
	require.Empty(t, agentA.approvedAnnotationsFor("apps", "example"))
	require.NoError(t, storeA.Close())

	storeB := openBoltAnnotationStore(t, path)
	t.Cleanup(func() { _ = storeB.Close() })
	agentB := annotationAgentWithStore(t, &recordingEngine{release: annotationRelease()}, storeB)

	assert.Empty(t, agentB.approvedAnnotationsFor("apps", "example"),
		"a cleared whitelist must not come back after a restart")
	items := agentB.buildWorkloadIdentityItems(t.Context(), annotationRelease())
	require.Len(t, items, 1)
	assert.Empty(t, items[0].GetCurrentAnnotations(),
		"a restarted operator must not report annotations a release write cleared")
}

// TASK-244 isolation: two releases persisted separately must hydrate into their
// own cache entries after a restart, never into one shared whitelist.
func TestAgent_AnnotationWhitelistRestartKeepsReleasesIsolated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator.db")

	storeA := openBoltAnnotationStore(t, path)
	agentA := annotationAgentWithStore(t, &recordingEngine{release: annotationRelease()}, storeA)

	alpha := installCommand("cmd-restart-alpha")
	alpha.Namespace = "apps"
	alpha.ReleaseName = "alpha"
	alpha.ApprovedAnnotationKeys = []*operatorv1.ApprovedAnnotationKey{{Key: "team", Scope: "WORKLOAD_METADATA"}}
	require.NoError(t, agentA.handleCommand(t.Context(), newTestStream(), alpha))

	beta := installCommand("cmd-restart-beta")
	beta.Namespace = "apps"
	beta.ReleaseName = "beta"
	beta.ApprovedAnnotationKeys = []*operatorv1.ApprovedAnnotationKey{{Key: "tier", Scope: "WORKLOAD_METADATA"}}
	require.NoError(t, agentA.handleCommand(t.Context(), newTestStream(), beta))
	require.NoError(t, storeA.Close())

	storeB := openBoltAnnotationStore(t, path)
	t.Cleanup(func() { _ = storeB.Close() })
	agentB := annotationAgentWithStore(t, &recordingEngine{release: annotationRelease()}, storeB)

	alphaItems := agentB.buildWorkloadIdentityItems(t.Context(), annotationReleaseNamed("alpha"))
	require.Len(t, alphaItems, 1)
	assert.Equal(t, map[string]map[string]string{"WORKLOAD_METADATA": {"team": "platform"}},
		flattenScopedAnnotations(alphaItems[0].GetCurrentAnnotations()),
		"alpha must hydrate its own whitelist")

	betaItems := agentB.buildWorkloadIdentityItems(t.Context(), annotationReleaseNamed("beta"))
	require.Len(t, betaItems, 1)
	assert.Equal(t, map[string]map[string]string{"WORKLOAD_METADATA": {"tier": "web"}},
		flattenScopedAnnotations(betaItems[0].GetCurrentAnnotations()),
		"beta must hydrate its own whitelist, not alpha's")
}

// TASK-244 graceful degradation: a Store that does not implement
// AnnotationWhitelistStore (the in-package memoryStore fake, and any older
// implementation) must still construct an Agent, keep the in-memory cache
// working, and never panic or error.
func TestAgent_AnnotationWhitelistPersistenceUnsupportedStaysInMemory(t *testing.T) {
	agent := annotationAgent(t)
	assert.Nil(t, agent.annotationWhitelistStore,
		"a store without the persistence interface must be a silent no-op")

	command := installCommand("cmd-unsupported-store")
	command.ApprovedAnnotationKeys = annotationWhitelist()
	require.NoError(t, agent.handleCommand(t.Context(), newTestStream(), command))
	assert.NotEmpty(t, agent.approvedAnnotationsFor("apps", "example"),
		"the in-memory cache must still work without persistence")
}
