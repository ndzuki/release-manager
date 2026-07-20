package preflight

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
)

func classifyPullFailure(pod *corev1.Pod) (code, reason string, failed bool) {
	if pod == nil {
		return "", "", false
	}
	for i := range pod.Status.ContainerStatuses {
		status := &pod.Status.ContainerStatuses[i]
		waiting := status.State.Waiting
		if waiting == nil {
			continue
		}
		if code, ok := classifyPullMessage(waiting.Reason + ": " + waiting.Message); ok {
			return code, sanitizePullReason(waiting.Message), true
		}
	}
	for _, condition := range pod.Status.Conditions {
		if code, ok := classifyPullMessage(condition.Reason + ": " + condition.Message); ok {
			return code, sanitizePullReason(condition.Message), true
		}
	}
	return "", "", false
}

func classifyPullMessage(message string) (string, bool) {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "accessdenied"),
		strings.Contains(lower, "iam"),
		strings.Contains(lower, "assume role"),
		strings.Contains(lower, "not authorized to perform"):
		return ErrIAMDenied, true
	case strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "denied: requested access"),
		strings.Contains(lower, "authentication required"),
		strings.Contains(lower, "no basic auth credentials"):
		return ErrRegistryUnauthorized, true
	case strings.Contains(lower, "i/o timeout"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "network is unreachable"),
		strings.Contains(lower, "no such host"),
		strings.Contains(lower, "tls handshake timeout"):
		return ErrNetworkUnreachable, true
	case strings.Contains(lower, "imagepullbackoff"),
		strings.Contains(lower, "errimagepull"),
		strings.Contains(lower, "failed to pull image"):
		return ErrImagePullBackOff, true
	default:
		return "", false
	}
}

func imagePulled(pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	for i := range pod.Status.ContainerStatuses {
		status := &pod.Status.ContainerStatuses[i]
		if status.Name != "pull-probe" {
			continue
		}
		return status.State.Running != nil || status.State.Terminated != nil
	}
	return false
}

func sanitizePullReason(reason string) string {
	reason = strings.TrimSpace(strings.Join(strings.Fields(reason), " "))
	if len(reason) > maxPullReasonBytes {
		reason = reason[:maxPullReasonBytes] + "..."
	}
	return reason
}
