package operator

import (
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ktesting "k8s.io/client-go/testing"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmergencyCommandExecutorSetContainerImage(t *testing.T) {
	deployment := emergencyDeploymentFixture()
	client := kubernetesfake.NewSimpleClientset(deployment)
	executor := NewEmergencyCommandExecutor(client)
	command := emergencyCommandFixture()
	command.Change = &operatorv1.EmergencyCommand_SetContainerImage{SetContainerImage: &operatorv1.EmergencySetContainerImage{
		Container: "app", ImageReference: "registry.example/app@sha256:new",
	}}

	result, err := executor.Execute(t.Context(), command)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"before":{"workload_uid":"uid-api","container":"app","image_reference":"registry.example/app@sha256:old"},
		"after":{"workload_uid":"uid-api","container":"app","image_reference":"registry.example/app@sha256:new"}
	}`, result)
	updated, err := client.AppsV1().Deployments("apps").Get(t.Context(), "api", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "registry.example/app@sha256:new", updated.Spec.Template.Spec.Containers[0].Image)
}

func TestEmergencyCommandExecutorSetReplicas(t *testing.T) {
	deployment := emergencyDeploymentFixture()
	client := kubernetesfake.NewSimpleClientset(deployment)
	executor := NewEmergencyCommandExecutor(client)
	command := emergencyCommandFixture()
	command.Change = &operatorv1.EmergencyCommand_SetReplicas{SetReplicas: &operatorv1.EmergencySetReplicas{Replicas: 5}}

	result, err := executor.Execute(t.Context(), command)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"before":{"workload_uid":"uid-api","replicas":2},
		"after":{"workload_uid":"uid-api","replicas":5}
	}`, result)
	updated, err := client.AppsV1().Deployments("apps").Get(t.Context(), "api", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, updated.Spec.Replicas)
	assert.Equal(t, int32(5), *updated.Spec.Replicas)
}

func TestEmergencyCommandExecutorSetApprovedAnnotations(t *testing.T) {
	deployment := emergencyDeploymentFixture()
	client := kubernetesfake.NewSimpleClientset(deployment)
	executor := NewEmergencyCommandExecutor(client)
	command := emergencyCommandFixture()
	command.Change = &operatorv1.EmergencyCommand_SetApprovedAnnotations{SetApprovedAnnotations: &operatorv1.EmergencySetApprovedAnnotations{
		Scope: "POD_TEMPLATE_METADATA",
		Entries: []*operatorv1.EmergencyAnnotationEntry{{Key: "example.com/incident-id", Value: "INC-42"}},
	}}

	result, err := executor.Execute(t.Context(), command)
	require.NoError(t, err)
	assert.Contains(t, result, `"value":"INC-42"`)
	updated, err := client.AppsV1().Deployments("apps").Get(t.Context(), "api", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "INC-42", updated.Spec.Template.Annotations["example.com/incident-id"])
}

func TestEmergencyCommandExecutorRejectsUIDMismatch(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset(emergencyDeploymentFixture())
	executor := NewEmergencyCommandExecutor(client)
	command := emergencyCommandFixture()
	command.WorkloadUid = "other-uid"
	command.Change = &operatorv1.EmergencyCommand_SetReplicas{SetReplicas: &operatorv1.EmergencySetReplicas{Replicas: 5}}

	_, err := executor.Execute(t.Context(), command)
	require.Error(t, err)
	var coded *EmergencyExecutionError
	require.True(t, errors.As(err, &coded))
	assert.Equal(t, "workload_uid_mismatch", coded.ErrorCode())
}

func TestEmergencyCommandExecutorReturnsConflict(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset(emergencyDeploymentFixture())
	client.PrependReactor("update", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, "api", errors.New("conflict"))
	})
	executor := NewEmergencyCommandExecutor(client)
	command := emergencyCommandFixture()
	command.Change = &operatorv1.EmergencyCommand_SetReplicas{SetReplicas: &operatorv1.EmergencySetReplicas{Replicas: 5}}

	_, err := executor.Execute(t.Context(), command)
	require.Error(t, err)
	var coded *EmergencyExecutionError
	require.True(t, errors.As(err, &coded))
	assert.Equal(t, "resource_version_conflict", coded.ErrorCode())
}

func emergencyDeploymentFixture() *appsv1.Deployment {
	replicas := int32(2)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "apps", UID: types.UID("uid-api"), ResourceVersion: "7"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.example/app@sha256:old"}}},
			},
		},
	}
}

func emergencyCommandFixture() *operatorv1.EmergencyCommand {
	return &operatorv1.EmergencyCommand{
		CommandId: "command-1", OperationId: "operation-1", WorkloadKind: emergencyDeployment,
		WorkloadName: "api", WorkloadNamespace: "apps", WorkloadUid: "uid-api",
	}
}
