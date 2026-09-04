package operator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	operatorv1 "github.com/ndzuki/release-manager/api/gen/operator/v1"
	"github.com/ndzuki/release-manager/internal/store"
)

// manifestWorkloadKinds maps the native manifest Kind spellings produced by
// helmengine.ExtractWorkloads (REQ-077) to the enum-style vocabulary shared
// with EmergencyCommand.workload_kind (REQ-032/079). Job is deliberately
// absent — it is outside the emergency workload whitelist (D-110 ①).
var manifestWorkloadKinds = map[string]string{
	"Deployment":  emergencyDeployment,
	"StatefulSet": emergencyStatefulSet,
	"DaemonSet":   emergencyDaemonSet,
}

// NormalizeWorkloadKind converts a manifest Kind spelling to the enum-style
// workload kind used across the emergency contract. The second return reports
// whether the kind is within the emergency whitelist (DEPLOYMENT /
// STATEFUL_SET / DAEMON_SET).
func NormalizeWorkloadKind(kind string) (string, bool) {
	normalized, ok := manifestWorkloadKinds[kind]
	return normalized, ok
}

// workloadObject is the minimal live-object view shared between the emergency
// executor and workload identity reporting (REQ-085).
type workloadObject struct {
	uid         string
	deployment  *appsv1.Deployment
	statefulSet *appsv1.StatefulSet
	daemonSet   *appsv1.DaemonSet
}

// readWorkloadObject performs the typed client-go read for an enum-style
// workload kind. It is the single kind→resource dispatch point shared by the
// emergency executor (loadWorkload) and identity reporting (WorkloadUID) so
// the kind mapping cannot drift between read paths (D-110 ①).
func readWorkloadObject(ctx context.Context, client kubernetes.Interface, kind, namespace, name string) (*workloadObject, error) {
	switch kind {
	case emergencyDeployment:
		resource, err := client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return &workloadObject{uid: string(resource.UID), deployment: resource}, nil
	case emergencyStatefulSet:
		resource, err := client.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return &workloadObject{uid: string(resource.UID), statefulSet: resource}, nil
	case emergencyDaemonSet:
		resource, err := client.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return &workloadObject{uid: string(resource.UID), daemonSet: resource}, nil
	default:
		return nil, emergencyExecutionError("workload_kind_not_supported", errors.New("workload kind is unsupported"))
	}
}

// WorkloadUID reads the live workload object for the given enum-style kind
// and returns its Kubernetes UID (REQ-085 D-110 ①). Unsupported kinds yield
// the workload_kind_not_supported error code; callers treat any error as
// "identity not observable" and fail closed (skip the report item).
func WorkloadUID(ctx context.Context, client kubernetes.Interface, kind, namespace, name string) (string, error) {
	if client == nil {
		return "", errors.New("kubernetes client is unavailable")
	}
	object, err := readWorkloadObject(ctx, client, kind, namespace, name)
	if err != nil {
		return "", err
	}
	return object.uid, nil
}

// applyWorkloadIdentityReport persists the reported authoritative identities
// onto the matching release_inventory rows (REQ-085 D-110 ②). Items are
// scoped to the reporting operator's (customer, cluster) and matched by the
// release key; rows the inventory does not know are ignored. When the row's
// definition carries promotion mappings, only the item matching a mapping's
// (kind, name) is selected; without mappings exactly one item per release is
// accepted — anything else keeps the identity empty (fail closed). Items
// with an empty uid/kind/name are dropped.
func (s *Service) applyWorkloadIdentityReport(ctx context.Context, report *operatorv1.WorkloadIdentityReport, operatorID string) error {
	if report == nil || len(report.GetItems()) == 0 {
		return nil
	}
	op, err := s.store.Operators().Get(ctx, operatorID)
	if err != nil {
		return fmt.Errorf("resolve reporting operator: %w", err)
	}
	rows, err := s.store.Inventories().ListByCluster(ctx, op.CustomerID, op.ClusterID)
	if err != nil {
		return fmt.Errorf("list inventory for identity report: %w", err)
	}
	byRelease := make(map[string]*store.ReleaseInventory, len(rows))
	for _, row := range rows {
		byRelease[reportReleaseKey(row.Namespace, row.ReleaseName)] = row
	}
	// Group the report items by release key so the per-release selection
	// rule below sees the whole candidate set at once.
	groups := make(map[string][]*operatorv1.WorkloadIdentityItem)
	for _, item := range report.GetItems() {
		if item == nil {
			continue
		}
		key := reportReleaseKey(item.GetReleaseNamespace(), item.GetReleaseName())
		groups[key] = append(groups[key], item)
	}
	for key, items := range groups {
		row, ok := byRelease[key]
		if !ok {
			s.logger.Debug("ignoring workload identity for unknown release", "namespace_release", key)
			continue
		}
		identity, ok, err := selectWorkloadIdentity(ctx, s.store, row, items)
		if err != nil {
			s.logger.Warn("workload identity selection failed", "namespace_release", key, "error", err)
			continue
		}
		if !ok {
			s.logger.Debug("workload identity not selectable, keeping fail-closed empty identity", "namespace_release", key)
			continue
		}
		if err := s.store.Inventories().UpdateWorkloadIdentity(ctx, op.CustomerID, op.ClusterID, row.Namespace, row.ReleaseName, identity); err != nil {
			s.logger.Warn("failed to persist workload identity", "namespace_release", key, "error", err)
			continue
		}
	}
	return nil
}

// selectWorkloadIdentity picks the authoritative identity item for one
// inventory row (REQ-085 D-110 ②). Selection is fail closed: an ambiguous or
// unmapped report never overwrites an existing identity; a definition lookup
// failure surfaces as an error so the caller logs it instead of silently
// falling back to the weaker selection rule.
func selectWorkloadIdentity(ctx context.Context, st store.Store, row *store.ReleaseInventory, items []*operatorv1.WorkloadIdentityItem) (store.WorkloadIdentity, bool, error) {
	complete := make([]*operatorv1.WorkloadIdentityItem, 0, len(items))
	for _, item := range items {
		if item.GetKind() == "" || item.GetName() == "" || item.GetUid() == "" || item.GetNamespace() == "" {
			continue // incomplete items are never selectable (fail closed)
		}
		complete = append(complete, item)
	}
	if len(complete) == 0 {
		return store.WorkloadIdentity{}, false, nil
	}
	identityOf := func(item *operatorv1.WorkloadIdentityItem) store.WorkloadIdentity {
		return store.WorkloadIdentity{Kind: item.GetKind(), Name: item.GetName(), Namespace: item.GetNamespace(), UID: item.GetUid()}
	}
	if row.ReleaseDefinitionID != "" {
		definition, err := st.Definitions().Get(ctx, row.ReleaseDefinitionID)
		if err != nil {
			return store.WorkloadIdentity{}, false, fmt.Errorf("load definition for identity selection: %w", err)
		}
		if len(definition.PromotionMappings) > 0 {
			// Promotion mappings carry the only kind source for the
			// definition (D1=B): the mapped workload is the emergency target.
			for _, item := range complete {
				for _, mapping := range definition.PromotionMappings {
					if item.GetKind() == mapping.WorkloadKind && item.GetName() == mapping.WorkloadName {
						return identityOf(item), true, nil
					}
				}
			}
			return store.WorkloadIdentity{}, false, nil
		}
	}
	if len(complete) != 1 {
		return store.WorkloadIdentity{}, false, nil // ambiguous without mappings
	}
	return identityOf(complete[0]), true, nil
}

// reportReleaseKey formats the stable release key used by both sides of the
// identity report matching.
func reportReleaseKey(namespace, releaseName string) string {
	return strings.TrimSpace(namespace) + "/" + strings.TrimSpace(releaseName)
}
