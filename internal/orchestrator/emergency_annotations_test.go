package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// seedApprovedAnnotationKeys puts the whitelist on the definition the emergency
// fixture uses. The whitelist is the authority the request is checked against.
func seedApprovedAnnotationKeys(t *testing.T, st store.Store, keys ...store.ApprovedAnnotationKey) {
	t.Helper()
	definition, err := st.Definitions().Get(t.Context(), "def-001")
	require.NoError(t, err)
	definition.ApprovedAnnotationKeys = keys
	_, err = st.Definitions().Update(t.Context(), definition, nil)
	require.NoError(t, err)
}

func annotationRequest(key string) *connect.Request[orchestratorv1.ExecuteEmergencyChangeRequest] {
	return connect.NewRequest(&orchestratorv1.ExecuteEmergencyChangeRequest{
		ReleaseDefinitionId: "def-001",
		WorkloadRef:         "deployments/default/api",
		ConvergenceStrategy: orchestratorv1.ConvergenceStrategy_REVERT_ON_NEXT_RECONCILE,
		IdempotencyKey:      key,
		AnnotationScope:     "deployment",
		Annotations:         []*orchestratorv1.AnnotationEntry{{Key: "team", Value: "payments"}},
	})
}

// TASK-126 AC 1/5: the request may only set keys the definition approved, in the
// scope that key was approved for. Each rejection is a stable code, and nothing
// is persisted.
func TestExecuteEmergencyChangeAnnotationValidation(t *testing.T) {
	whitelist := []store.ApprovedAnnotationKey{{Key: "team", Scope: "deployment", PromotionValuesPath: "metadata.annotations.team"}}

	cases := map[string]struct {
		whitelist []store.ApprovedAnnotationKey
		mutate    func(*orchestratorv1.ExecuteEmergencyChangeRequest)
		wantCode  string
	}{
		"key not on the whitelist": {
			whitelist: whitelist,
			mutate:    func(r *orchestratorv1.ExecuteEmergencyChangeRequest) { r.Annotations[0].Key = "forged" },
			wantCode:  "annotation_key_not_allowed",
		},
		"key repeated": {
			whitelist: whitelist,
			mutate: func(r *orchestratorv1.ExecuteEmergencyChangeRequest) {
				r.Annotations = append(r.Annotations, &orchestratorv1.AnnotationEntry{Key: "team", Value: "other"})
			},
			wantCode: "duplicate_annotation_key",
		},
		"scope does not match the whitelist entry": {
			whitelist: whitelist,
			mutate:    func(r *orchestratorv1.ExecuteEmergencyChangeRequest) { r.AnnotationScope = "namespace" },
			wantCode:  "annotation_scope_mismatch",
		},
		"empty key": {
			whitelist: whitelist,
			mutate:    func(r *orchestratorv1.ExecuteEmergencyChangeRequest) { r.Annotations[0].Key = " " },
			wantCode:  "invalid_annotation_entries",
		},
		"empty entries": {
			whitelist: whitelist,
			mutate:    func(r *orchestratorv1.ExecuteEmergencyChangeRequest) { r.Annotations = nil },
			wantCode:  "invalid_annotation_entries",
		},
		"empty scope": {
			whitelist: whitelist,
			mutate:    func(r *orchestratorv1.ExecuteEmergencyChangeRequest) { r.AnnotationScope = "" },
			wantCode:  "invalid_annotation_entries",
		},
		"no approved keys at all": {
			whitelist: nil,
			mutate:    func(*orchestratorv1.ExecuteEmergencyChangeRequest) {},
			wantCode:  "annotation_key_not_allowed",
		},
		// AC-058-13 bounds: the batch is 1..50 and each value 1..2048 bytes.
		// Only the empty batch was checked before, so 51 entries and an
		// oversized value reached the operator.
		"too many annotations": {
			whitelist: whitelist,
			mutate: func(r *orchestratorv1.ExecuteEmergencyChangeRequest) {
				r.Annotations = make([]*orchestratorv1.AnnotationEntry, 0, 51)
				for i := 0; i < 51; i++ {
					r.Annotations = append(r.Annotations, &orchestratorv1.AnnotationEntry{Key: "team", Value: "payments"})
				}
			},
			wantCode: "invalid_annotation_entries",
		},
		"annotation value too long": {
			whitelist: whitelist,
			mutate: func(r *orchestratorv1.ExecuteEmergencyChangeRequest) {
				r.Annotations[0].Value = strings.Repeat("a", 2049)
			},
			wantCode: "invalid_annotation_entries",
		},
		"annotation value empty": {
			whitelist: whitelist,
			mutate: func(r *orchestratorv1.ExecuteEmergencyChangeRequest) {
				r.Annotations[0].Value = ""
			},
			wantCode: "invalid_annotation_entries",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc, st, dispatcher := emergencyTestService(t)
			seedApprovedAnnotationKeys(t, st, tc.whitelist...)
			req := annotationRequest("annotation-" + name)
			tc.mutate(req.Msg)

			_, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), req)
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			assert.Equal(t, tc.wantCode, connectErrorReason(err), "TASK-126 AC 1: the stable code must be emitted")
			assert.Empty(t, dispatcher.commands, "TASK-126 AC 5: a rejected request must dispatch nothing")
		})
	}
}

// TASK-126 AC 1/3: an approved key is accepted, and its entries reach the intent
// (the dispatch path unmarshals exactly this shape).
func TestExecuteEmergencyChangeAnnotationAccepted(t *testing.T) {
	svc, st, dispatcher := emergencyTestService(t)
	seedApprovedAnnotationKeys(t, st, store.ApprovedAnnotationKey{
		Key: "team", Scope: "deployment", PromotionValuesPath: "metadata.annotations.team",
	})

	resp, err := svc.ExecuteEmergencyChange(emergencyAdminContext(), annotationRequest("annotation-accepted"))
	require.NoError(t, err)
	require.Len(t, dispatcher.commands, 1)

	command := dispatcher.commands[0].GetSetApprovedAnnotations()
	require.NotNil(t, command, "the annotation action must dispatch an annotation command")
	assert.Equal(t, "deployment", command.GetScope())
	require.Len(t, command.GetEntries(), 1)
	assert.Equal(t, "team", command.GetEntries()[0].GetKey())
	assert.Equal(t, "payments", command.GetEntries()[0].GetValue())

	intent, err := st.EmergencyIntents().GetByOperationID(t.Context(), resp.Msg.GetOperationId())
	require.NoError(t, err)
	require.NotNil(t, intent.AnnotationScope)
	assert.Equal(t, "deployment", *intent.AnnotationScope)
	var stored []emergencyAnnotationEntry
	require.NoError(t, json.Unmarshal(intent.AnnotationEntries, &stored))
	require.Len(t, stored, 1, "the entries must be persisted, not left empty")
	assert.Equal(t, emergencyAnnotationEntry{Key: "team", Value: "payments"}, stored[0])
}
