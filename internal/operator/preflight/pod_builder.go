package preflight

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var digestImagePattern = regexp.MustCompile(`^.+@sha256:[a-fA-F0-9]{64}$`)

var approvedProbeCommands = map[string]struct{}{
	"/bin/true":     {},
	"/usr/bin/true": {},
	"true":          {},
}

func validatePullInput(input PullInput) error {
	if strings.TrimSpace(input.OperationID) == "" || strings.TrimSpace(input.Namespace) == "" {
		return fmt.Errorf("%w: operation_id and namespace are required", ErrPullInputInvalid)
	}
	if len(input.Images) == 0 || len(input.Images) > MaxPullImages {
		return fmt.Errorf("%w: image count must be between 1 and %d", ErrPullInputInvalid, MaxPullImages)
	}
	for _, image := range input.Images {
		if !digestImagePattern.MatchString(strings.TrimSpace(image)) {
			return fmt.Errorf("%w: %q", ErrUnpinnedImage, image)
		}
	}
	if len(input.ProbeCommand) > 1 {
		return fmt.Errorf("%w: arguments are not allowed", ErrUntrustedProbeCommand)
	}
	if len(input.ProbeCommand) == 1 {
		if _, ok := approvedProbeCommands[input.ProbeCommand[0]]; !ok {
			return fmt.Errorf("%w: %q", ErrUntrustedProbeCommand, input.ProbeCommand[0])
		}
	}
	switch input.CleanupPolicy {
	case "", CleanupAlways, CleanupOnSuccess, CleanupBackground:
		return nil
	default:
		return fmt.Errorf("%w: unsupported cleanup policy %q", ErrPullInputInvalid, input.CleanupPolicy)
	}
}

func buildProbePod(input PullInput, image string, now time.Time) *corev1.Pod {
	imageID := stableID(image)
	operationID := stableID(input.OperationID)
	command := input.ProbeCommand
	if len(command) == 0 {
		command = []string{"/bin/true"}
	}
	graceSeconds := int64(0)
	runAsNonRoot := true
	readOnlyRootFilesystem := true
	allowPrivilegeEscalation := false
	seccompProfile := &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "rm-pull-" + imageID[:10] + "-",
			Namespace:    input.Namespace,
			Labels: map[string]string{
				ManagedLabel:   "true",
				OperationLabel: operationID,
				ImageLabel:     imageID,
			},
			Annotations: map[string]string{
				ExpireAtAnnotation: now.Add(DefaultProbeTTL).UTC().Format(time.RFC3339),
			},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName:            input.ServiceAccount,
			AutomountServiceAccountToken:  boolPtr(false),
			RestartPolicy:                 corev1.RestartPolicyNever,
			TerminationGracePeriodSeconds: &graceSeconds,
			EnableServiceLinks:            boolPtr(false),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   &runAsNonRoot,
				SeccompProfile: seccompProfile,
			},
			Containers: []corev1.Container{
				{
					Name:            "pull-probe",
					Image:           image,
					ImagePullPolicy: corev1.PullAlways,
					Command:         command,
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1m"),
							corev1.ResourceMemory: resource.MustParse("4Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("10m"),
							corev1.ResourceMemory: resource.MustParse("16Mi"),
						},
					},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: &allowPrivilegeEscalation,
						ReadOnlyRootFilesystem:   &readOnlyRootFilesystem,
						RunAsNonRoot:             &runAsNonRoot,
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
					},
				},
			},
		},
	}
}

func stableID(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func boolPtr(value bool) *bool {
	return &value
}
