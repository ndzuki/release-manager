package orchestrator

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// annotatedFreshObservation is freshObservation plus the two-scope approved
// annotation projection the operator reports (TASK-241 U1=A/U2=B).
func annotatedFreshObservation(now time.Time) emergencyObservation {
	observation := freshObservation(now)
	observation.Annotations = map[string]map[string]string{
		"WORKLOAD_METADATA":     {"team": "platform"},
		"POD_TEMPLATE_METADATA": {"prometheus.io/scrape": "true"},
	}
	return observation
}

func listTarget(t *testing.T, svc *Service) *orchestratorv1.EmergencyTarget {
	t.Helper()
	resp, err := svc.ListEmergencyTargets(deployerCtx(), connect.NewRequest(&orchestratorv1.ListEmergencyTargetsRequest{
		ReleaseDefinitionId: "def-001",
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTargets(), 1)
	return resp.Msg.GetTargets()[0]
}

// TASK-241 W-d: a fresh observation carries the approved annotations, grouped
// by scope on the wire as "<scope>/<key>" (the read-model proto field is a flat
// map). The annotation operation is advertised only because the observation is
// fresh and non-empty. Removing the annotation projection or the freshness gate
// in emergency_queries.go makes the assertions fail.
//
// TASK-247: the read path re-filters against the definition's current
// whitelist, so the fixture must grant the two keys the observation carries —
// production can only persist a projection the definition approves.
func TestListEmergencyTargetsProjectsFreshAnnotations(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)
	seedApprovedAnnotationKeys(t, st,
		store.ApprovedAnnotationKey{Key: "team", Scope: "WORKLOAD_METADATA"},
		store.ApprovedAnnotationKey{Key: "prometheus.io/scrape", Scope: "POD_TEMPLATE_METADATA"},
	)
	seedObservedInventory(t, st, annotatedFreshObservation(time.Now().UTC()))

	target := listTarget(t, svc)

	assert.Equal(t, map[string]string{
		// Kubernetes annotation keys may contain '/', so the scope separator is
		// the first '/' in the map key.
		"WORKLOAD_METADATA/team":                     "platform",
		"POD_TEMPLATE_METADATA/prometheus.io/scrape": "true",
	}, target.GetCurrentAnnotations())
	assert.Contains(t, target.GetSupportedOperations(), orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_APPROVED_ANNOTATION)
}

// W-d fail-closed: a stale observation must never be projected as the current
// annotation value, and must not advertise the annotation operation. Removing
// the 15-minute staleness check makes this test fail.
func TestListEmergencyTargetsFailsClosedOnStaleAnnotations(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)
	seedObservedInventory(t, st, annotatedFreshObservation(time.Now().UTC().Add(-16*time.Minute)))

	target := listTarget(t, svc)

	assert.Empty(t, target.GetCurrentAnnotations())
	assert.NotContains(t, target.GetSupportedOperations(), orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_APPROVED_ANNOTATION)
	// A stale observation still keeps the replicas operation (definition-derived).
	assert.Contains(t, target.GetSupportedOperations(), orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_REPLICAS)
}

// W-d fail-closed: no observation at all (old operator, or a row without
// observed fields) is treated exactly like a stale one.
func TestListEmergencyTargetsFailsClosedWithoutAnnotations(t *testing.T) {
	svc, st, cleanup := setupService(t)
	defer cleanup()
	seedDefinition(t, st)
	seedReplicasDefinition(t, st, replicasPromotionMapping(), false, 10)

	target := listTarget(t, svc)

	assert.Empty(t, target.GetCurrentAnnotations())
	assert.NotContains(t, target.GetSupportedOperations(), orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_APPROVED_ANNOTATION)
}

func TestFlatScopedAnnotations(t *testing.T) {
	assert.Equal(t, map[string]string{
		"WORKLOAD_METADATA/team":                     "platform",
		"POD_TEMPLATE_METADATA/prometheus.io/scrape": "true",
	}, flatScopedAnnotations(map[string]map[string]string{
		"WORKLOAD_METADATA":     {"team": "platform"},
		"POD_TEMPLATE_METADATA": {"prometheus.io/scrape": "true"},
	}), "scope and key are joined with the first '/'")
	assert.NotNil(t, flatScopedAnnotations(nil), "the sentinel is an empty map, not nil")
	assert.Empty(t, flatScopedAnnotations(nil))
}

// projectEmergencyWorkload must deep-copy the annotation projection so a read
// model cannot mutate the store row it came from.
func TestProjectEmergencyWorkloadClonesAnnotations(t *testing.T) {
	now := time.Now().UTC()
	source := map[string]map[string]string{"WORKLOAD_METADATA": {"team": "platform"}}
	view := projectEmergencyWorkload(emergencyObservation{Annotations: source, ObservedAt: now}, true, now)
	require.True(t, view.Observed)
	view.Annotations["WORKLOAD_METADATA"]["team"] = "changed"
	assert.Equal(t, "platform", source["WORKLOAD_METADATA"]["team"], "the store row must not be aliased")
}
