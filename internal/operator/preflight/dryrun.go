package preflight

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// preflightFieldManager is the field manager every dry-run write is attributed
// to. It is constant so a dry-run can never be confused with a real write.
const preflightFieldManager = "release-manager-preflight"

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

// DryRunOne performs a single resource dry-run against the target cluster. It
// returns a ResourceResult containing only safe fields.
//
// The caller must provide the parsed unstructured object. The executor resolves
// the GVR from the mapper, determines namespace/scope, and executes the dry-run
// with DryRunAll set. DryRunAuto probes the object first so the call matches its
// lifecycle (Update when it exists, Create otherwise); DryRunUpdate also reads
// the current resourceVersion. No option ever writes to the cluster.
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

	resourceCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	client := e.mapper.ResourceClient(gvr, rr.Namespace)
	sanitized := sanitizeResource(obj)

	result, err := e.dryRunWrite(resourceCtx, client, sanitized, option)

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

// dryRunWrite performs the one server-side dry-run write for an object.
//
// DryRunAuto picks the semantics from the object's lifecycle: an existing object
// is dry-run Updated, a missing one is dry-run Created. This matters because the
// API server rejects a Create for a name that already exists (AlreadyExists), so
// a preflight that always Creates can only ever pass before the release's first
// install — every UPGRADE would fail its cluster stage (ADR-027 restores the full
// preflight for UPGRADE). DryRunUpdate requires the current resourceVersion,
// which is read here and set on the object that is actually sent (sanitizeResource
// returns a copy for Secrets).
//
// Every branch sends DryRun=All: the request is validated by the API server and
// never persisted.
func (e *DryRunExecutor) dryRunWrite(
	ctx context.Context,
	client dynamic.ResourceInterface,
	obj *unstructured.Unstructured,
	option DryRunOption,
) (*unstructured.Unstructured, error) {
	createOpts := metav1.CreateOptions{DryRun: []string{dryRunAll}, FieldManager: preflightFieldManager}
	updateOpts := metav1.UpdateOptions{DryRun: []string{dryRunAll}, FieldManager: preflightFieldManager}

	switch option {
	case DryRunCreate:
		return client.Create(ctx, obj, createOpts)
	case DryRunUpdate, DryRunAuto:
		existing, err := client.Get(ctx, obj.GetName(), metav1.GetOptions{})
		if err != nil {
			// Only the auto option may fall back to Create, and only for a
			// genuinely absent object. Any other read failure fails closed: we
			// cannot prove which semantics the apply will need.
			if option == DryRunAuto && apierrors.IsNotFound(err) {
				return client.Create(ctx, obj, createOpts)
			}
			return nil, err
		}
		obj.SetResourceVersion(existing.GetResourceVersion())
		return client.Update(ctx, obj, updateOpts)
	}
	return nil, fmt.Errorf("unsupported dry-run option %d", option)
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

		// DryRunAuto matches each object's lifecycle: an object a previous
		// install already created is dry-run Updated, a new one Created. Both
		// are server-side dry-runs, so the API server still validates the write.
		rr := e.DryRunOne(ctx, obj, DryRunAuto)

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
