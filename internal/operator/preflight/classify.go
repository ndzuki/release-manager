package preflight

// ErrorCodeFromAPIError classifies a Kubernetes API error into a stable
// preflight error code. It uses ReasonForError and the helper predicates
// from k8s.io/apimachinery/pkg/api/errors to map server responses.
//
// Classification priority (first match wins):
//  1. Namespace missing (checked via text) → namespace_missing
//  2. NoMatchError → api_not_supported
//  3. Admission webhook (text contains "webhook" or "admission") → admission_rejected
//  4. Quota exceeded (text contains "quota" or "exceeded quota") → quota_exceeded
//  5. Dry-run unsupported (MethodNotAllowed or Invalid + "dryrun") → dryrun_unavailable
//  6. Forbidden → kubernetes_forbidden
//  7. Anything else → preflight_unknown
func ErrorCodeFromAPIError(err error) string {
	if err == nil {
		return ""
	}

	msg := err.Error()

	// 1. Namespace missing.
	if isNamespaceNotFound(err) {
		return ErrNamespaceMissing
	}

	// 2. GVK not known to the cluster.
	if isNoMatch(err) {
		return ErrAPINotSupported
	}

	// 3. Admission webhook rejection.
	if isAdmissionRejected(err, msg) {
		return ErrAdmissionRejected
	}

	// 4. Quota exceeded.
	if isQuotaExceeded(err, msg) {
		return ErrQuotaExceeded
	}

	// 5. Dry-run not supported.
	if isDryRunUnavailable(err) {
		return ErrDryRunUnavailable
	}

	// 6. RBAC Forbidden (AC-047-02) — last among classified errors
	// since admission/quota also return 403.
	if isKubernetesForbidden(err) {
		return ErrKubernetesForbidden
	}

	return ErrUnknown
}
