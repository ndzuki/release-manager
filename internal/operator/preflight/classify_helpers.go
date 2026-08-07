package preflight

import (
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
)

// isKubernetesForbidden detects pure RBAC Forbidden responses
// (not admission webhook or quota).
func isKubernetesForbidden(err error) bool {
	return apierrors.IsForbidden(err)
}

// isNamespaceNotFound detects a missing namespace.
func isNamespaceNotFound(err error) bool {
	if !apierrors.IsNotFound(err) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "namespaces") && strings.Contains(msg, "not found")
}

// isDryRunUnavailable detects when the server does not support dry-run.
func isDryRunUnavailable(err error) bool {
	if apierrors.IsMethodNotSupported(err) {
		return true
	}
	if apierrors.IsInvalid(err) {
		msg := strings.ToLower(err.Error())
		return strings.Contains(msg, "dryrun")
	}
	return false
}

// isAdmissionRejected detects admission webhook rejection.
func isAdmissionRejected(_ error, msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "webhook") ||
		strings.Contains(lower, "admission")
}

// isQuotaExceeded detects resource quota violations.
func isQuotaExceeded(_ error, msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "exceeded quota") ||
		strings.Contains(lower, "quota")
}

// isNoMatch detects when a GVK is not known to the cluster.
func isNoMatch(err error) bool {
	return meta.IsNoMatchError(err)
}
