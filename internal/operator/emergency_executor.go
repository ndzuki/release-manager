package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
)

const (
	emergencyDeployment  = "DEPLOYMENT"
	emergencyStatefulSet = "STATEFUL_SET"
	emergencyDaemonSet   = "DAEMON_SET"
)

// EmergencyExecutionError carries a stable result code for the control stream.
type EmergencyExecutionError struct {
	code string
	err  error
}

func (e *EmergencyExecutionError) Error() string { return e.err.Error() }
func (e *EmergencyExecutionError) Unwrap() error { return e.err }
func (e *EmergencyExecutionError) ErrorCode() string {
	return e.code
}

// EmergencyCommandExecutor applies one allow-listed Kubernetes field update.
type EmergencyCommandExecutor struct {
	client kubernetes.Interface
}

// NewEmergencyCommandExecutor creates a typed client-go emergency executor.
func NewEmergencyCommandExecutor(client kubernetes.Interface) *EmergencyCommandExecutor {
	return &EmergencyCommandExecutor{client: client}
}

// NewKubernetesClient loads in-cluster or kubeconfig credentials and creates a typed clientset.
func NewKubernetesClient(kubeConfig string) (kubernetes.Interface, error) {
	cfg, err := kubernetesRESTConfig(kubeConfig)
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create Kubernetes client: %w", err)
	}
	return client, nil
}

func kubernetesRESTConfig(kubeConfig string) (*rest.Config, error) {
	if strings.TrimSpace(kubeConfig) != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeConfig)
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig: %w", err)
		}
		return cfg, nil
	}
	cfg, err := rest.InClusterConfig()
	if err == nil {
		return cfg, nil
	}
	cfg, fallbackErr := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if fallbackErr != nil {
		return nil, fmt.Errorf("load Kubernetes config: %w", errors.Join(err, fallbackErr))
	}
	return cfg, nil
}

type emergencyWorkload struct {
	uid                string
	containers         *[]corev1.Container
	replicas           **int32
	workloadAnnotations *map[string]string
	podAnnotations      *map[string]string
	update              func(context.Context) error
}

type emergencySnapshotEnvelope struct {
	Before any `json:"before"`
	After  any `json:"after"`
}

type emergencyImageSnapshot struct {
	WorkloadUID   string `json:"workload_uid"`
	Container     string `json:"container"`
	ImageReference string `json:"image_reference"`
}

type emergencyReplicasSnapshot struct {
	WorkloadUID string `json:"workload_uid"`
	Replicas    int32  `json:"replicas"`
}

type emergencyAnnotationSnapshot struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type emergencyAnnotationsSnapshot struct {
	WorkloadUID string                        `json:"workload_uid"`
	Scope       string                        `json:"scope"`
	Annotations []emergencyAnnotationSnapshot `json:"annotations"`
}

// Execute verifies workload identity, applies one typed update, and returns sanitized before/after JSON.
//nolint:gocyclo // emergency executor maps 3 oneof branches through common load/apply/snapshot flow.
func (e *EmergencyCommandExecutor) Execute(ctx context.Context, command *operatorv1.EmergencyCommand) (string, error) {
	if e == nil || e.client == nil {
		return "", emergencyExecutionError("kubernetes_client_unavailable", errors.New("kubernetes client is unavailable"))
	}
	if command == nil || command.GetCommandId() == "" || command.GetWorkloadName() == "" || command.GetWorkloadNamespace() == "" || command.GetWorkloadUid() == "" {
		return "", emergencyExecutionError("invalid_command", errors.New("emergency command target is invalid"))
	}
	workload, err := e.loadWorkload(ctx, command)
	if err != nil {
		return "", err
	}
	if workload.uid != command.GetWorkloadUid() {
		return "", emergencyExecutionError("workload_uid_mismatch", errors.New("workload UID does not match"))
	}

	var envelope emergencySnapshotEnvelope
	switch change := command.GetChange().(type) {
	case *operatorv1.EmergencyCommand_SetContainerImage:
		before, after, applyErr := applyEmergencyImage(workload, change.SetContainerImage)
		if applyErr != nil {
			return "", applyErr
		}
		envelope.Before, envelope.After = before, after
	case *operatorv1.EmergencyCommand_SetReplicas:
		before, after, applyErr := applyEmergencyReplicas(workload, change.SetReplicas)
		if applyErr != nil {
			return "", applyErr
		}
		envelope.Before, envelope.After = before, after
	case *operatorv1.EmergencyCommand_SetApprovedAnnotations:
		before, after, applyErr := applyEmergencyAnnotations(workload, change.SetApprovedAnnotations)
		if applyErr != nil {
			return "", applyErr
		}
		envelope.Before, envelope.After = before, after
	default:
		return "", emergencyExecutionError("unsupported_emergency_action", errors.New("emergency action is unsupported"))
	}

	if err := workload.update(ctx); err != nil {
		return "", classifyEmergencyUpdateError(err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", emergencyExecutionError("result_encoding_failed", fmt.Errorf("encode emergency result: %w", err))
	}
	return string(encoded), nil
}

func (e *EmergencyCommandExecutor) loadWorkload(ctx context.Context, command *operatorv1.EmergencyCommand) (*emergencyWorkload, error) {
	namespace := command.GetWorkloadNamespace()
	name := command.GetWorkloadName()
	switch command.GetWorkloadKind() {
	case emergencyDeployment:
		resource, err := e.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, classifyEmergencyGetError(err)
		}
		return deploymentWorkload(e.client, resource), nil
	case emergencyStatefulSet:
		resource, err := e.client.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, classifyEmergencyGetError(err)
		}
		return statefulSetWorkload(e.client, resource), nil
	case emergencyDaemonSet:
		resource, err := e.client.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, classifyEmergencyGetError(err)
		}
		return daemonSetWorkload(e.client, resource), nil
	default:
		return nil, emergencyExecutionError("workload_kind_not_supported", errors.New("workload kind is unsupported"))
	}
}

func deploymentWorkload(client kubernetes.Interface, resource *appsv1.Deployment) *emergencyWorkload {
	return &emergencyWorkload{
		uid: string(resource.UID), containers: &resource.Spec.Template.Spec.Containers, replicas: &resource.Spec.Replicas,
		workloadAnnotations: &resource.Annotations, podAnnotations: &resource.Spec.Template.Annotations,
		update: func(ctx context.Context) error {
			_, err := client.AppsV1().Deployments(resource.Namespace).Update(ctx, resource, metav1.UpdateOptions{})
			return err
		},
	}
}

func statefulSetWorkload(client kubernetes.Interface, resource *appsv1.StatefulSet) *emergencyWorkload {
	return &emergencyWorkload{
		uid: string(resource.UID), containers: &resource.Spec.Template.Spec.Containers, replicas: &resource.Spec.Replicas,
		workloadAnnotations: &resource.Annotations, podAnnotations: &resource.Spec.Template.Annotations,
		update: func(ctx context.Context) error {
			_, err := client.AppsV1().StatefulSets(resource.Namespace).Update(ctx, resource, metav1.UpdateOptions{})
			return err
		},
	}
}

func daemonSetWorkload(client kubernetes.Interface, resource *appsv1.DaemonSet) *emergencyWorkload {
	return &emergencyWorkload{
		uid: string(resource.UID), containers: &resource.Spec.Template.Spec.Containers,
		workloadAnnotations: &resource.Annotations, podAnnotations: &resource.Spec.Template.Annotations,
		update: func(ctx context.Context) error {
			_, err := client.AppsV1().DaemonSets(resource.Namespace).Update(ctx, resource, metav1.UpdateOptions{})
			return err
		},
	}
}

func applyEmergencyImage(workload *emergencyWorkload, change *operatorv1.EmergencySetContainerImage) (any, any, error) {
	if change == nil || strings.TrimSpace(change.GetContainer()) == "" || strings.TrimSpace(change.GetImageReference()) == "" {
		return nil, nil, emergencyExecutionError("invalid_command", errors.New("container and image reference are required"))
	}
	for index := range *workload.containers {
		container := &(*workload.containers)[index]
		if container.Name != change.GetContainer() {
			continue
		}
		before := emergencyImageSnapshot{WorkloadUID: workload.uid, Container: container.Name, ImageReference: container.Image}
		container.Image = change.GetImageReference()
		after := emergencyImageSnapshot{WorkloadUID: workload.uid, Container: container.Name, ImageReference: container.Image}
		return before, after, nil
	}
	return nil, nil, emergencyExecutionError("container_not_found", errors.New("container was not found in workload"))
}

func applyEmergencyReplicas(workload *emergencyWorkload, change *operatorv1.EmergencySetReplicas) (any, any, error) {
	if change == nil || workload.replicas == nil {
		return nil, nil, emergencyExecutionError("workload_kind_not_supported", errors.New("workload does not support replicas"))
	}
	beforeReplicas := int32(1)
	if *workload.replicas != nil {
		beforeReplicas = **workload.replicas
	}
	before := emergencyReplicasSnapshot{WorkloadUID: workload.uid, Replicas: beforeReplicas}
	replicas := change.GetReplicas()
	*workload.replicas = &replicas
	after := emergencyReplicasSnapshot{WorkloadUID: workload.uid, Replicas: replicas}
	return before, after, nil
}

func applyEmergencyAnnotations(workload *emergencyWorkload, change *operatorv1.EmergencySetApprovedAnnotations) (any, any, error) {
	if change == nil || len(change.GetEntries()) == 0 {
		return nil, nil, emergencyExecutionError("invalid_command", errors.New("annotation entries are required"))
	}
	var annotations *map[string]string
	switch change.GetScope() {
	case "WORKLOAD_METADATA":
		annotations = workload.workloadAnnotations
	case "POD_TEMPLATE_METADATA":
		annotations = workload.podAnnotations
	default:
		return nil, nil, emergencyExecutionError("annotation_scope_invalid", errors.New("annotation scope is invalid"))
	}
	if *annotations == nil {
		*annotations = make(map[string]string, len(change.GetEntries()))
	}
	beforeEntries := make([]emergencyAnnotationSnapshot, 0, len(change.GetEntries()))
	afterEntries := make([]emergencyAnnotationSnapshot, 0, len(change.GetEntries()))
	for _, entry := range change.GetEntries() {
		if entry == nil || entry.GetKey() == "" {
			return nil, nil, emergencyExecutionError("invalid_command", errors.New("annotation key is required"))
		}
		beforeEntries = append(beforeEntries, emergencyAnnotationSnapshot{Key: entry.GetKey(), Value: (*annotations)[entry.GetKey()]})
		(*annotations)[entry.GetKey()] = entry.GetValue()
		afterEntries = append(afterEntries, emergencyAnnotationSnapshot{Key: entry.GetKey(), Value: entry.GetValue()})
	}
	return emergencyAnnotationsSnapshot{WorkloadUID: workload.uid, Scope: change.GetScope(), Annotations: beforeEntries},
		emergencyAnnotationsSnapshot{WorkloadUID: workload.uid, Scope: change.GetScope(), Annotations: afterEntries}, nil
}

func classifyEmergencyGetError(err error) error {
	if apierrors.IsNotFound(err) {
		return emergencyExecutionError("workload_not_found", errors.New("workload was not found"))
	}
	if apierrors.IsForbidden(err) {
		return emergencyExecutionError("forbidden", errors.New("operator is not authorized to read workload"))
	}
	return emergencyExecutionError("kubernetes_get_failed", fmt.Errorf("get workload: %w", err))
}

func classifyEmergencyUpdateError(err error) error {
	if apierrors.IsConflict(err) {
		return emergencyExecutionError("resource_version_conflict", errors.New("workload resource version conflicted"))
	}
	if apierrors.IsNotFound(err) {
		return emergencyExecutionError("workload_not_found", errors.New("workload was not found"))
	}
	if apierrors.IsForbidden(err) {
		return emergencyExecutionError("forbidden", errors.New("operator is not authorized to update workload"))
	}
	return emergencyExecutionError("kubernetes_update_failed", fmt.Errorf("update workload: %w", err))
}

func emergencyExecutionError(code string, err error) error {
	return &EmergencyExecutionError{code: code, err: err}
}
