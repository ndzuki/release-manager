package agent

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/operator/helmengine"
	"github.com/ndzuki/release-manager/internal/store"
)

// annotationDeployment is the live workload the TASK-241 tests read: two
// containers (the projection must be independent of container count) plus
// approved and unapproved annotations on both scopes.
func annotationDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api", Namespace: "apps", UID: types.UID("uid-annotation-1"),
			Annotations: map[string]string{
				"team":              "platform",
				"tier":              "web",
				"secret-annotation": "must-never-leave-the-cluster",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
					"prometheus.io/scrape": "true",
					"secret-pod":           "must-never-leave-the-cluster",
				}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "api", Image: "registry.example/team/api:1.0.0"},
					{Name: "sidecar", Image: "registry.example/team/sidecar:2.0.0"},
				}},
			},
		},
	}
}

func annotationRelease() *helmengine.Release {
	return &helmengine.Release{
		Name: "example", Namespace: "apps", Revision: 1, Status: "deployed",
		ManifestDigest: "sha256:manifest",
		Workloads:      []helmengine.WorkloadSummary{{Kind: "Deployment", Name: "api", Namespace: "apps"}},
	}
}

func annotationAgent(t *testing.T) *Agent {
	t.Helper()
	return annotationAgentWithEngine(t, &recordingEngine{release: annotationRelease()})
}

// annotationAgentWithEngine is annotationAgent with a caller-supplied engine, so
// a test whose command path needs Status/History can script those responses.
func annotationAgentWithEngine(t *testing.T, engine helmengine.Engine) *Agent {
	t.Helper()
	agent, err := New(Config{
		Client: noopClient{}, Engine: engine, Store: newMemoryStore(),
		SessionID: "session-1", OperatorID: "operator-1",
		KubeClient:   kubernetesfake.NewSimpleClientset(annotationDeployment()),
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		InstallFlags: InstallFlags{Atomic: true, Timeout: time.Minute},
	})
	require.NoError(t, err)
	return agent
}

// annotationReleaseNamed returns annotationRelease() under a different release
// name, keeping the same live workload (namespace apps, deployment api) so only
// the cache key varies between releases.
func annotationReleaseNamed(name string) *helmengine.Release {
	release := annotationRelease()
	release.Namespace = "apps"
	release.Name = name
	return release
}

// annotationWhitelist is the approved subset: TEAM/TIER on the workload object
// and the Prometheus scrape hint on the pod template. The unapproved live keys
// (`secret-annotation`, `secret-pod`) must never appear in a report.
func annotationWhitelist() []*operatorv1.ApprovedAnnotationKey {
	return []*operatorv1.ApprovedAnnotationKey{
		{Key: "team", Scope: "WORKLOAD_METADATA"},
		{Key: "tier", Scope: "WORKLOAD_METADATA"},
		{Key: "prometheus.io/scrape", Scope: "POD_TEMPLATE_METADATA"},
	}
}

// flattenScopedAnnotations turns the wire shape into scope → key → value so an
// assertion states the grouping without depending on the message order.
func flattenScopedAnnotations(scoped []*operatorv1.ScopedAnnotations) map[string]map[string]string {
	out := make(map[string]map[string]string, len(scoped))
	for _, group := range scoped {
		entries := make(map[string]string, len(group.GetEntries()))
		for _, entry := range group.GetEntries() {
			entries[entry.GetKey()] = entry.GetValue()
		}
		out[group.GetScope()] = entries
	}
	return out
}

func workloadIdentityReports(stream *testStream) []*operatorv1.WorkloadIdentityReport {
	reports := make([]*operatorv1.WorkloadIdentityReport, 0, 1)
	for _, sent := range stream.sent {
		if report := sent.GetWorkloadIdentityReport(); report != nil {
			reports = append(reports, report)
		}
	}
	return reports
}

// TASK-241 U1=A + U2=B: the operator projects only the approved annotation keys
// and groups them by scope. The cluster carries unapproved keys on both scopes;
// they must not appear in the report (negative control 1).
func TestAgent_AnnotationObservationProjectsOnlyApprovedKeysByScope(t *testing.T) {
	agent := annotationAgent(t)
	stream := newTestStream()

	command := installCommand("cmd-annotation-approved")
	command.ApprovedAnnotationKeys = annotationWhitelist()
	require.NoError(t, agent.handleCommand(t.Context(), stream, command))

	reports := workloadIdentityReports(stream)
	require.Len(t, reports, 1)
	require.Len(t, reports[0].GetItems(), 1)
	item := reports[0].GetItems()[0]
	assert.Equal(t, []string{"api", "sidecar"}, item.GetContainers(), "the container projection is unchanged")

	got := flattenScopedAnnotations(item.GetCurrentAnnotations())
	want := map[string]map[string]string{
		"WORKLOAD_METADATA":     {"team": "platform", "tier": "web"},
		"POD_TEMPLATE_METADATA": {"prometheus.io/scrape": "true"},
	}
	assert.Equal(t, want, got, "only approved keys, grouped by scope")

	// Negative control 1: an unapproved key is present in the cluster but must
	// never leave it.
	assert.NotContains(t, got["WORKLOAD_METADATA"], "secret-annotation")
	assert.NotContains(t, got["POD_TEMPLATE_METADATA"], "secret-pod")
	for _, group := range item.GetCurrentAnnotations() {
		for _, entry := range group.GetEntries() {
			assert.NotEqual(t, "must-never-leave-the-cluster", entry.GetValue(),
				"an unapproved annotation value reached the wire")
		}
	}
}

// Negative control 2 (TASK-241 fail closed): without a release-write command
// the whitelist is unknown, so the observation reports no annotations at all —
// even though the cluster carries approved-looking keys.
func TestAgent_AnnotationObservationWithoutCommandReportsNothing(t *testing.T) {
	agent := annotationAgent(t)

	items := agent.buildWorkloadIdentityItems(t.Context(), annotationRelease())
	require.Len(t, items, 1)
	assert.Empty(t, items[0].GetCurrentAnnotations(),
		"an unknown whitelist must report no annotations (fail closed)")
}

// A release write without a whitelist clears a previously cached one: removing
// every approved key center-side must stop the observation, not leave a stale
// whitelist reporting values.
func TestAgent_AnnotationObservationReleaseWriteWithoutWhitelistClearsCache(t *testing.T) {
	agent := annotationAgent(t)

	first := newTestStream()
	withWhitelist := installCommand("cmd-annotation-clear-1")
	withWhitelist.ApprovedAnnotationKeys = annotationWhitelist()
	require.NoError(t, agent.handleCommand(t.Context(), first, withWhitelist))
	require.Len(t, workloadIdentityReports(first), 1)
	require.NotEmpty(t, workloadIdentityReports(first)[0].GetItems()[0].GetCurrentAnnotations())

	second := newTestStream()
	withoutWhitelist := installCommand("cmd-annotation-clear-2")
	require.NoError(t, agent.handleCommand(t.Context(), second, withoutWhitelist))

	reports := workloadIdentityReports(second)
	require.Len(t, reports, 1)
	assert.Empty(t, reports[0].GetItems()[0].GetCurrentAnnotations(),
		"a release write with no approved keys must clear the cached whitelist")
}

// The cache is refreshed on the replay path too: a command replayed from the
// local store still carries its whitelist and must keep the observation alive.
func TestAgent_AnnotationObservationReplayKeepsWhitelist(t *testing.T) {
	agent := annotationAgent(t)
	stream := newTestStream()

	command := installCommand("cmd-annotation-replay")
	command.ApprovedAnnotationKeys = annotationWhitelist()
	require.NoError(t, agent.handleCommand(t.Context(), stream, command))

	// Drop the in-memory cache, then replay the persisted entry: the replay
	// path must rebuild it from the stored payload.
	agent.approvedAnnotationsMu.Lock()
	agent.approvedAnnotations = map[string][]store.ApprovedAnnotationKey{}
	agent.approvedAnnotationsMu.Unlock()

	entry, err := agent.store.Get(t.Context(), "cmd-annotation-replay")
	require.NoError(t, err)
	replayStream := newTestStream()
	require.NoError(t, agent.executeEntry(t.Context(), replayStream, entry))

	reports := workloadIdentityReports(replayStream)
	require.Len(t, reports, 1)
	assert.Equal(t, map[string]map[string]string{
		"WORKLOAD_METADATA":     {"team": "platform", "tier": "web"},
		"POD_TEMPLATE_METADATA": {"prometheus.io/scrape": "true"},
	}, flattenScopedAnnotations(reports[0].GetItems()[0].GetCurrentAnnotations()))
}

// A non-release command must not clear the cached whitelist: an inventory sync
// describes no definition and would otherwise make a healthy release flap to
// "whitelist unknown".
func TestAgent_AnnotationObservationNonReleaseCommandKeepsCache(t *testing.T) {
	agent := annotationAgent(t)

	stream := newTestStream()
	command := installCommand("cmd-annotation-keep")
	command.ApprovedAnnotationKeys = annotationWhitelist()
	require.NoError(t, agent.handleCommand(t.Context(), stream, command))

	syncStream := newTestStream()
	require.NoError(t, agent.handleCommand(t.Context(), syncStream, &operatorv1.Command{
		CommandId: "cmd-inventory-sync", OperationType: "INVENTORY_SYNC",
		Namespace: "apps", ReleaseName: "example",
	}))

	assert.NotEmpty(t, agent.approvedAnnotationsFor("apps", "example"),
		"a non-release command must not clear the whitelist")
}

// TASK-245 (a): UPGRADE is a release write (isReleaseWrite), so it refreshes the
// cached annotation whitelist for its release. A preceding INSTALL cached a
// different whitelist; the UPGRADE's observation must project the new one,
// otherwise "refresh" is indistinguishable from "reuse the stale cache".
func TestAgent_AnnotationObservationUpgradeRefreshesWhitelist(t *testing.T) {
	valuesJSON := []byte(`{"message":"hello"}`)
	engine := &recordingEngine{release: annotationRelease(), status: annotationRelease()}
	agent := annotationAgentWithEngine(t, engine)

	stale := installCommand("cmd-annotation-upgrade-stale")
	stale.ApprovedAnnotationKeys = []*operatorv1.ApprovedAnnotationKey{{Key: "team", Scope: "WORKLOAD_METADATA"}}
	require.NoError(t, agent.handleCommand(t.Context(), newTestStream(), stale))
	require.Equal(t, []store.ApprovedAnnotationKey{{Key: "team", Scope: "WORKLOAD_METADATA"}},
		agent.approvedAnnotationsFor("apps", "example"),
		"precondition: the INSTALL cached the stale whitelist")

	stream := newTestStream()
	command := upgradeCommand("cmd-annotation-upgrade-refresh", valuesJSON, sha256Hex(valuesJSON))
	// DecodeCommandPayload mirrors the envelope identity onto the wire command,
	// so the test sets the top-level fields the operator reads for the cache key.
	command.Namespace = "apps"
	command.ReleaseName = "example"
	command.ApprovedAnnotationKeys = []*operatorv1.ApprovedAnnotationKey{{Key: "tier", Scope: "WORKLOAD_METADATA"}}
	require.NoError(t, agent.handleCommand(t.Context(), stream, command))

	reports := workloadIdentityReports(stream)
	require.Len(t, reports, 1)
	assert.Equal(t, map[string]map[string]string{"WORKLOAD_METADATA": {"tier": "web"}},
		flattenScopedAnnotations(reports[0].GetItems()[0].GetCurrentAnnotations()),
		"an UPGRADE must refresh the cached whitelist, not reuse the INSTALL one")
}

// TASK-245 (a): ROLLBACK is the third release write and must refresh the cache
// the same way. The rollback succeeds against a history containing the target
// revision, so a workload identity report is emitted and can be asserted.
func TestAgent_AnnotationObservationRollbackRefreshesWhitelist(t *testing.T) {
	engine := &recordingEngine{
		release: annotationRelease(),
		history: []helmengine.ReleaseHistoryEntry{{Revision: 1, Status: "superseded"}},
	}
	agent := annotationAgentWithEngine(t, engine)

	stale := installCommand("cmd-annotation-rollback-stale")
	stale.ApprovedAnnotationKeys = []*operatorv1.ApprovedAnnotationKey{{Key: "team", Scope: "WORKLOAD_METADATA"}}
	require.NoError(t, agent.handleCommand(t.Context(), newTestStream(), stale))
	require.Equal(t, []store.ApprovedAnnotationKey{{Key: "team", Scope: "WORKLOAD_METADATA"}},
		agent.approvedAnnotationsFor("apps", "example"),
		"precondition: the INSTALL cached the stale whitelist")

	stream := newTestStream()
	command := rollbackCommand("cmd-annotation-rollback-refresh")
	command.ApprovedAnnotationKeys = []*operatorv1.ApprovedAnnotationKey{{Key: "tier", Scope: "WORKLOAD_METADATA"}}
	require.NoError(t, agent.handleCommand(t.Context(), stream, command))

	reports := workloadIdentityReports(stream)
	require.Len(t, reports, 1)
	assert.Equal(t, map[string]map[string]string{"WORKLOAD_METADATA": {"tier": "web"}},
		flattenScopedAnnotations(reports[0].GetItems()[0].GetCurrentAnnotations()),
		"a ROLLBACK must refresh the cached whitelist, not reuse the INSTALL one")
}

// TASK-245 (b): the approved-annotation cache is keyed by release identity, so
// two releases with different whitelists must each observe through their own
// whitelist. A global (release-identity-ignoring) cache would leak beta's
// whitelist into alpha's observation and vice versa.
func TestAgent_AnnotationObservationIsolatesWhitelistPerRelease(t *testing.T) {
	agent := annotationAgent(t)

	alpha := installCommand("cmd-annotation-alpha")
	alpha.Namespace = "apps"
	alpha.ReleaseName = "alpha"
	alpha.ApprovedAnnotationKeys = []*operatorv1.ApprovedAnnotationKey{{Key: "team", Scope: "WORKLOAD_METADATA"}}
	require.NoError(t, agent.handleCommand(t.Context(), newTestStream(), alpha))

	beta := installCommand("cmd-annotation-beta")
	beta.Namespace = "apps"
	beta.ReleaseName = "beta"
	beta.ApprovedAnnotationKeys = []*operatorv1.ApprovedAnnotationKey{{Key: "tier", Scope: "WORKLOAD_METADATA"}}
	require.NoError(t, agent.handleCommand(t.Context(), newTestStream(), beta))

	alphaItems := agent.buildWorkloadIdentityItems(t.Context(), annotationReleaseNamed("alpha"))
	require.Len(t, alphaItems, 1)
	assert.Equal(t, map[string]map[string]string{"WORKLOAD_METADATA": {"team": "platform"}},
		flattenScopedAnnotations(alphaItems[0].GetCurrentAnnotations()),
		"release alpha must observe through its own whitelist")

	betaItems := agent.buildWorkloadIdentityItems(t.Context(), annotationReleaseNamed("beta"))
	require.Len(t, betaItems, 1)
	assert.Equal(t, map[string]map[string]string{"WORKLOAD_METADATA": {"tier": "web"}},
		flattenScopedAnnotations(betaItems[0].GetCurrentAnnotations()),
		"release beta must observe through its own whitelist")
}
