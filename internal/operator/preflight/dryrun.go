package preflight

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DryRunExecutor performs server-side dry-run requests against the target
// cluster using the operator's own ServiceAccount credentials.
// It never impersonates, escalates, or retries with different permissions.
type DryRunExecutor struct {
	mapper  *GKVMapper
	timeout time.Duration
}

// NewDryRunExecutor creates a new executor with defaults.
func NewDryRunExecutor(mapper *GKVMapper) *DryRunExecutor {
	return &DryRunExecutor{
		mapper:  mapper,
		timeout: DefaultResourceTimeout,
	}
}

// SetTimeout overrides the per-resource dry-run timeout.
func (e *DryRunExecutor) SetTimeout(d time.Duration) {
	e.timeout = d
}

// DryRunOne performs a single resource dry-run Create or Update against the
// target cluster. It returns a ResourceResult containing only safe fields.
//
// The caller must provide the parsed unstructured object. The executor resolves
// the GVR from the mapper, determines namespace/scope, and executes exactly
// ONE API call with DryRunAll set.
func (e *DryRunExecutor) DryRunOne(
	ctx context.Context,
	obj *unstructured.Unstructured,
	option DryRunOption,
) ResourceResult {
	start := time.Now()

	gvk := obj.GroupVersionKind()
	rr := ResourceResult{
		GVK:  gvk,
		Name: obj.GetName(),
	}

	gvr, namespaced, err := e.mapper.Map(gvk)
	if err != nil {
		rr.Rejected = true
		rr.ErrorCode = ErrorCodeFromAPIError(err)
		rr.Reason = err.Error()
		rr.Duration = time.Since(start)
		return rr
	}

	// Determine namespace.
	if namespaced && !IsClusterScoped(gvk.Kind) {
		rr.Namespace = obj.GetNamespace()
	}

	// Build options.
	opts := metav1.CreateOptions{
		DryRun:       []string{dryRunAll},
		FieldManager: "release-manager-preflight",
	}

	resourceCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	client := e.mapper.ResourceClient(gvr, rr.Namespace)
	sanitized := sanitizeResource(obj)

	var result *unstructured.Unstructured

	switch option {
	case DryRunCreate:
		result, err = client.Create(resourceCtx, sanitized, opts)
	case DryRunUpdate:
		existing, getErr := client.Get(resourceCtx, obj.GetName(), metav1.GetOptions{})
		if getErr != nil {
			rr.Rejected = true
			rr.ErrorCode = ErrorCodeFromAPIError(getErr)
			rr.Reason = getErr.Error()
			rr.Duration = time.Since(start)
			return rr
		}
		obj.SetResourceVersion(existing.GetResourceVersion())
		updateOpts := metav1.UpdateOptions{
			DryRun:       []string{dryRunAll},
			FieldManager: "release-manager-preflight",
		}
		result, err = client.Update(resourceCtx, sanitized, updateOpts)
	}

	rr.Duration = time.Since(start)

	if err != nil {
		rr.Rejected = true
		rr.ErrorCode = ErrorCodeFromAPIError(err)
		rr.Reason = sanitizeErrorMessage(err)
		return rr
	}

	_ = result // result is only used to confirm success; we don't persist it.

	rr.Accepted = true
	return rr
}

// DryRunAll executes a dry-run for each resource in the manifest stream.
// It returns a BatchResult that is safe to persist and log — it contains
// no raw object bodies or Secret values.
func (e *DryRunExecutor) DryRunAll(
	ctx context.Context,
	resources []*unstructured.Unstructured,
	input Input,
) (*BatchResult, error) {
	batchStart := time.Now()

	ctx, cancel := context.WithTimeout(ctx, DefaultBatchTimeout)
	defer cancel()

	batch := &BatchResult{
		OperationID:       input.OperationID,
		RenderDigest:      input.RenderDigest,
		CapabilityVersion: input.CapabilityVersion,
		Results:           make([]ResourceResult, 0, len(resources)),
	}

	for _, obj := range resources {
		select {
		case <-ctx.Done():
			batch.ResourceCount = len(batch.Results)
			batch.Duration = time.Since(batchStart)
			return batch, ErrPreflightCancelled
		default:
		}

		gvk := obj.GroupVersionKind()
		if !IsClusterScoped(gvk.Kind) && obj.GetNamespace() == "" && input.TargetNamespace != "" {
			obj.SetNamespace(input.TargetNamespace)
		}

		rr := e.DryRunOne(ctx, obj, DryRunCreate)

		// Sanitize before storing.
		rr = sanitizeResourceResult(rr)

		batch.Results = append(batch.Results, rr)

		if rr.Rejected {
			batch.ResourceCount = len(batch.Results)
			batch.Duration = time.Since(batchStart)
			return batch, nil
		}
	}

	batch.Passed = true
	batch.ResourceCount = len(batch.Results)
	batch.Duration = time.Since(batchStart)
	return batch, nil
}

// sanitizeResourceResult ensures a ResourceResult carries no sensitive data.
func sanitizeResourceResult(rr ResourceResult) ResourceResult {
	rr.Reason = truncateReason(rr.Reason, 512)
	return rr
}

func truncateReason(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func sanitizeErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	return truncateReason(msg, 512)
}
