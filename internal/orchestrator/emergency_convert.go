package orchestrator

import (
	orchestratorv1 "github.com/ndzuki/release-manager/api/gen/orchestrator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

func emergencyActionFromProto(a orchestratorv1.EmergencyAction) store.EmergencyAction {
	switch a {
	case orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_CONTAINER_IMAGE:
		return store.EmergencySetContainerImage
	case orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_REPLICAS:
		return store.EmergencySetReplicas
	case orchestratorv1.EmergencyAction_EMERGENCY_ACTION_SET_APPROVED_ANNOTATION:
		return store.EmergencySetApprovedAnnotation
	default:
		return ""
	}
}

func emergencyConvergenceFromProto(c orchestratorv1.EmergencyConvergence) store.EmergencyConvergence {
	switch c {
	case orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REQUIRE_PROMOTION:
		return store.EmergencyRequirePromotion
	case orchestratorv1.EmergencyConvergence_EMERGENCY_CONVERGENCE_REVERT_ON_NEXT_RECONCILE:
		return store.EmergencyRevertOnNextReconcile
	default:
		return store.EmergencyRequirePromotion
	}
}
