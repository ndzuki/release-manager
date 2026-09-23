package operator

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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
	uid, _, err := WorkloadObservation(ctx, client, kind, namespace, name)
	return uid, err
}

// WorkloadObservation reads the live workload object once and returns both the
// authoritative UID (REQ-085) and the observed field projection (TASK-168,
// REQ-058 C1/R1) that travels on the same identity report. It reuses
// readWorkloadObject, so the kind→resource dispatch stays single-sourced
// (D-110 ①) and the projection costs no extra cluster API call.
//
// ObservedAt is stamped from the local clock at read time. Absent replicas
// (DaemonSet, or a Deployment whose spec.replicas the API server never
// defaulted) stay nil — the store contract reads nil as "not observed" and must
// never confuse it with a real zero.
func WorkloadObservation(ctx context.Context, client kubernetes.Interface, kind, namespace, name string) (string, store.WorkloadObservation, error) {
	if client == nil {
		return "", store.WorkloadObservation{}, errors.New("kubernetes client is unavailable")
	}
	object, err := readWorkloadObject(ctx, client, kind, namespace, name)
	if err != nil {
		return "", store.WorkloadObservation{}, err
	}
	return object.uid, object.observation(time.Now().UTC()), nil
}

// observation projects the live object's mutable field values into the
// store.WorkloadObservation that the identity report carries. An object that
// carries no pod template containers still reports its observation time: the
// read succeeded, and the per-field sentinels ("no containers", "no replica
// count") keep the central read model fail-closed.
func (o *workloadObject) observation(observedAt time.Time) store.WorkloadObservation {
	observation := store.WorkloadObservation{ObservedAt: observedAt}
	switch {
	case o.deployment != nil:
		observation.Containers, observation.ImageRefs = projectContainers(o.deployment.Spec.Template.Spec.Containers)
		observation.Replicas = cloneReplicas(o.deployment.Spec.Replicas)
	case o.statefulSet != nil:
		observation.Containers, observation.ImageRefs = projectContainers(o.statefulSet.Spec.Template.Spec.Containers)
		observation.Replicas = cloneReplicas(o.statefulSet.Spec.Replicas)
	case o.daemonSet != nil:
		observation.Containers, observation.ImageRefs = projectContainers(o.daemonSet.Spec.Template.Spec.Containers)
		// A DaemonSet has no replica count: Replicas stays nil ("not observed").
	}
	return observation
}

// projectContainers projects the pod template containers into the container
// name list and the name→image map, preserving the read order.
func projectContainers(containers []corev1.Container) (names []string, images map[string]string) {
	names = make([]string, 0, len(containers))
	images = make(map[string]string, len(containers))
	for index := range containers {
		container := &containers[index]
		names = append(names, container.Name)
		images[container.Name] = container.Image
	}
	return names, images
}

// cloneReplicas copies the replica pointer so the report never aliases the
// informer/client-go cache object. A nil pointer stays nil ("not observed").
func cloneReplicas(replicas *int32) *int32 {
	if replicas == nil {
		return nil
	}
	value := *replicas
	return &value
}

// defaultPendingIdentityTTL bounds how long a buffered identity whose
// release_inventory row never appears stays pending before the sweep purges it
// (REQ-088 D3=A). 10 minutes is comfortably larger than the targeted 30s
// update window plus the 5-minute full-sync cycle plus retry headroom; tests
// override it with WithPendingIdentityTTL.
const defaultPendingIdentityTTL = 10 * time.Minute

// WorkloadIdentityResolution is the graded outcome of applying a reported
// identity to the current row identity (REQ-088 D4=C).
type WorkloadIdentityResolution int

const (
	// WorkloadIdentityBind: the row never observed an identity — apply the
	// reported four-tuple.
	WorkloadIdentityBind WorkloadIdentityResolution = iota
	// WorkloadIdentityNoop: the reported identity equals the current one.
	WorkloadIdentityNoop
	// WorkloadIdentityUpdateUID: the same workload (kind/name/namespace
	// equal) reports a new UID — a workload rebuild after the Deployment was
	// recreated — so the uid may be updated.
	WorkloadIdentityUpdateUID
	// WorkloadIdentityConflict: kind/name/namespace disagree or the report is
	// incomplete — fail closed, keep the existing identity.
	WorkloadIdentityConflict
)

// ResolveWorkloadIdentity grades a reported identity against the current row
// identity per REQ-088 D4=C. An empty current identity means the row has never
// observed one (bind); identical tuples no-op; an equal kind/name/namespace
// with a different uid is a workload rebuild (update uid); anything else
// conflicts and must keep the existing identity (fail closed, AC-088-03/07).
func ResolveWorkloadIdentity(current, report store.WorkloadIdentity) WorkloadIdentityResolution {
	if !identityObserved(current) {
		if identityComplete(report) {
			return WorkloadIdentityBind
		}
		// A report must never bind anything from an incomplete payload.
		return WorkloadIdentityConflict
	}
	if current == report {
		return WorkloadIdentityNoop
	}
	if !identityComplete(report) {
		// An incomplete report can never update or overwrite an existing
		// identity — fail closed.
		return WorkloadIdentityConflict
	}
	if current.Kind == report.Kind && current.Name == report.Name && current.Namespace == report.Namespace {
		return WorkloadIdentityUpdateUID
	}
	return WorkloadIdentityConflict
}

// identityObserved reports whether an inventory row has already persisted a
// workload identity (any of the four tuple fields set).
func identityObserved(identity store.WorkloadIdentity) bool {
	return identity.Kind != "" || identity.Name != "" || identity.Namespace != "" || identity.UID != ""
}

// identityComplete reports whether a reported identity carries every field of
// the four-tuple (kind/name/namespace/uid) — the precondition for binding.
func identityComplete(identity store.WorkloadIdentity) bool {
	return identity.Kind != "" && identity.Name != "" && identity.Namespace != "" && identity.UID != ""
}

// applyWorkloadIdentityReport converges the reported authoritative identities
// onto release_inventory (REQ-085 D-110 ② + REQ-088 ordering fix). Items are
// scoped to the reporting operator's (customer, cluster) and grouped by the
// release key. When the inventory row already exists the identity is applied
// with the D4=C tiered rule; when the row does not exist yet (the seed-first-
// operation ordering defect, REQ-088 background ②) a selectable four-tuple is
// buffered into pending_workload_identity instead of being dropped (D1=A /
// D2=A) and is replayed as soon as SyncInventory creates the row (D5=A).
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
	// Definitions keyed by release key feed selection both on the row-present
	// path (the row's promotion mappings) and on the row-absent path (reverse
	// lookup so a selectable four-tuple can be buffered, REQ-088 D2).
	defs, err := s.store.Definitions().List(ctx, op.CustomerID, op.ClusterID, true)
	if err != nil {
		return fmt.Errorf("list definitions for identity report: %w", err)
	}
	defByRelease := make(map[string]*store.ReleaseDefinition, len(defs))
	for _, def := range defs {
		defByRelease[reportReleaseKey(def.Namespace, def.ReleaseName)] = def
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
		// The CommandStream report handler and the SyncInventory replay path
		// run on different goroutines of the same control-plane process.
		// Every identity mutation for one release key happens under this
		// per-key lock, so a replay can never observe a half-applied report
		// (or vice versa) — the report-to-row and replay-to-row read-modify-
		// writes are serialized per release (REQ-088 D6 convergence).
		lock := s.identityStripeLock(op.CustomerID, op.ClusterID, items[0].GetReleaseNamespace(), items[0].GetReleaseName())
		lock.Lock()
		if row, ok := byRelease[key]; ok {
			s.applyReportToInventoryRow(ctx, op.CustomerID, op.ClusterID, row, defByRelease[key], items, key)
		} else {
			s.bufferWorkloadIdentityReport(ctx, op.CustomerID, op.ClusterID, defByRelease[key], key, items)
		}
		lock.Unlock()
	}
	return nil
}

// identityStripeLock returns the per-release-key stripe lock serializing all
// identity convergence for one release (report buffering / row-present apply /
// SyncInventory replay / sweep). A bounded stripe set keeps memory flat while
// unrelated releases almost never contend.
func (s *Service) identityStripeLock(customerID, clusterID, namespace, releaseName string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(customerID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(clusterID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(namespace))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(releaseName))
	return &s.identityLocks[h.Sum32()%uint32(len(s.identityLocks))]
}

// applyReportToInventoryRow applies one release's reported identity items to
// its existing inventory row. The row's own definition (from
// ReleaseDefinitionID) is the only kind source for promotion-mapped
// selection; a definition that cannot be loaded fails closed exactly as the
// pre-REQ-088 behavior did (identity stays empty). A D4=C conflict is a
// deterministic terminal state — the existing identity is kept and counted.
// The caller must hold the release-key identityStripeLock.
func (s *Service) applyReportToInventoryRow(ctx context.Context, customerID, clusterID string, row *store.ReleaseInventory, fallbackDef *store.ReleaseDefinition, items []*operatorv1.WorkloadIdentityItem, key string) {
	var definition *store.ReleaseDefinition
	if row.ReleaseDefinitionID != "" {
		def, err := s.store.Definitions().Get(ctx, row.ReleaseDefinitionID)
		if err != nil {
			s.logger.Warn("workload identity selection failed", "namespace_release", key, "error", err)
			s.countIdentityReportDropped()
			return
		}
		definition = def
	} else {
		definition = fallbackDef
	}
	item, ok := SelectWorkloadIdentityItem(definition, items)
	if !ok {
		s.logger.Debug("workload identity not selectable, keeping fail-closed empty identity", "namespace_release", key)
		s.countIdentityReportDropped()
		return
	}
	identity := store.WorkloadIdentity{
		Kind: item.GetKind(), Name: item.GetName(), Namespace: item.GetNamespace(), UID: item.GetUid(),
	}
	if _, err := s.applyIdentity(ctx, customerID, clusterID, row.Namespace, row.ReleaseName, rowWorkloadIdentity(row), identity, key); err != nil {
		s.logger.Warn("failed to persist workload identity", "namespace_release", key, "error", err)
		return
	}
	// TASK-168 W2: the same report item also carries the observed field
	// projection. It is applied only after the identity was accepted, and it is
	// scoped to the selected item — an observation from a workload the row was
	// not bound to must never advertise containers the target cannot address.
	s.applyWorkloadObservation(ctx, customerID, clusterID, row.Namespace, row.ReleaseName, item, key)
	// A selectable report applied to the existing row supersedes any buffered
	// pending identity for the same release. If a stale pending row were left
	// behind, the sweep could later replay an OLDER report over the fresher
	// row identity (regressing a uid update after a workload rebuild). Under
	// the per-key lock no concurrent buffer can replace it in between.
	if err := s.store.PendingWorkloadIdentities().DeleteByReleaseKey(ctx, customerID, clusterID, row.Namespace, row.ReleaseName); err != nil {
		s.logger.Debug("failed to clear superseded pending identity", "namespace_release", key, "error", err)
	}
}

// bufferWorkloadIdentityReport persists a selectable identity four-tuple for a
// release whose inventory row is not created yet (REQ-088 D2=A). The unique
// release key makes re-reporting idempotent (D6=A). Only a selectable tuple is
// buffered — ambiguous or incomplete reports are dropped fail closed
// (AC-088-03), matching the row-present selection rule. The caller must hold
// the release-key identityStripeLock.
func (s *Service) bufferWorkloadIdentityReport(ctx context.Context, customerID, clusterID string, definition *store.ReleaseDefinition, key string, items []*operatorv1.WorkloadIdentityItem) {
	identity, ok := SelectWorkloadIdentity(definition, items)
	if !ok {
		s.logger.Debug("workload identity not selectable for pending release, dropping fail-closed", "namespace_release", key)
		s.countIdentityReportDropped()
		return
	}
	item := items[0]
	pending := &store.PendingWorkloadIdentity{
		CustomerID:        customerID,
		ClusterID:         clusterID,
		Namespace:         strings.TrimSpace(item.GetReleaseNamespace()),
		ReleaseName:       strings.TrimSpace(item.GetReleaseName()),
		WorkloadKind:      identity.Kind,
		WorkloadName:      identity.Name,
		WorkloadNamespace: identity.Namespace,
		WorkloadUID:       identity.UID,
	}
	if err := s.store.PendingWorkloadIdentities().Upsert(ctx, pending); err != nil {
		s.logger.Warn("failed to buffer workload identity for pending release", "namespace_release", key, "error", err)
		return
	}
	s.logger.Info("buffered workload identity for release awaiting inventory row",
		"namespace_release", key, "workload_kind", identity.Kind,
		"workload_name", identity.Name, "workload_namespace", identity.Namespace)
	s.countIdentityReportBuffered()
}

// SelectWorkloadIdentity picks the authoritative identity item for one release
// given its definition (REQ-085 D-110 ②). Selection is fail closed: an
// ambiguous or unmapped report never yields an identity. When the definition
// is nil (no definition yet, or a definition without a mapping) exactly one
// complete item per release is accepted; with promotion mappings only the item
// matching a mapping's (kind, name) is selected. Items missing any of
// kind/name/namespace/uid are never selectable. Returns ok=false when no
// identity can be chosen.
func SelectWorkloadIdentity(definition *store.ReleaseDefinition, items []*operatorv1.WorkloadIdentityItem) (store.WorkloadIdentity, bool) {
	item, ok := SelectWorkloadIdentityItem(definition, items)
	if !ok {
		return store.WorkloadIdentity{}, false
	}
	return store.WorkloadIdentity{
		Kind: item.GetKind(), Name: item.GetName(), Namespace: item.GetNamespace(), UID: item.GetUid(),
	}, true
}

// SelectWorkloadIdentityItem is the selection behind SelectWorkloadIdentity,
// returning the selected report item itself. The observation persisted with
// the identity must come from the *same* item the identity was selected from:
// persisting the observed fields of an unmapped workload onto the row would
// advertise containers the emergency target cannot actually address
// (TASK-168 W2).
func SelectWorkloadIdentityItem(definition *store.ReleaseDefinition, items []*operatorv1.WorkloadIdentityItem) (*operatorv1.WorkloadIdentityItem, bool) {
	complete := make([]*operatorv1.WorkloadIdentityItem, 0, len(items))
	for _, item := range items {
		if item == nil || item.GetKind() == "" || item.GetName() == "" || item.GetUid() == "" || item.GetNamespace() == "" {
			continue // incomplete items are never selectable (fail closed)
		}
		complete = append(complete, item)
	}
	if len(complete) == 0 {
		return nil, false
	}
	if definition != nil && len(definition.PromotionMappings) > 0 {
		// Promotion mappings carry the only kind source for the definition:
		// the mapped workload is the emergency target.
		for _, item := range complete {
			for _, mapping := range definition.PromotionMappings {
				if item.GetKind() == mapping.WorkloadKind && item.GetName() == mapping.WorkloadName {
					return item, true
				}
			}
		}
		return nil, false
	}
	if len(complete) != 1 {
		return nil, false // ambiguous without mappings
	}
	return complete[0], true
}

// applyIdentity persists a reported identity onto the inventory row located by
// its unique key using the REQ-088 D4=C tiered rule. current is the row's
// identity snapshot (possibly empty when never observed). Conflicts keep the
// existing identity (no write) and are counted; the returned resolution lets
// replay callers decide whether the buffered pending row is terminal.
func (s *Service) applyIdentity(ctx context.Context, customerID, clusterID, namespace, releaseName string, current, report store.WorkloadIdentity, key string) (WorkloadIdentityResolution, error) {
	resolution := ResolveWorkloadIdentity(current, report)
	switch resolution {
	case WorkloadIdentityBind, WorkloadIdentityUpdateUID:
		if err := s.store.Inventories().UpdateWorkloadIdentity(ctx, customerID, clusterID, namespace, releaseName, report); err != nil {
			return resolution, fmt.Errorf("persist workload identity: %w", err)
		}
		s.logger.Info("bound workload identity", "namespace_release", key,
			"workload_kind", report.Kind, "workload_name", report.Name,
			"workload_namespace", report.Namespace, "workload_uid", report.UID,
			"resolution", resolution)
	case WorkloadIdentityNoop:
		s.logger.Debug("workload identity unchanged, no-op", "namespace_release", key)
	case WorkloadIdentityConflict:
		s.logger.Warn("workload identity conflict; keeping existing identity (fail closed)",
			"namespace_release", key, "reported_kind", report.Kind, "reported_name", report.Name,
			"reported_namespace", report.Namespace, "existing_kind", current.Kind, "existing_name", current.Name)
		s.countIdentityConflict()
	}
	return resolution, nil
}

// applyWorkloadObservation persists the observed field projection carried by
// the selected identity item onto the release_inventory row (TASK-168 W2,
// REQ-058 C1/R1). It is the central-side half of the data plane: without it the
// observed_* columns stay NULL and the emergency read model keeps answering
// "current fields not observed" forever.
//
// A report without observed_at carries no observation at all — an older
// operator, or an item built before the projection existed. It must not
// overwrite an existing observation with the zero value (that would make a
// previously fresh row permanently stale), so it is a no-op. Failures are
// best-effort and never interrupt the control stream: the periodic observation
// report re-delivers on the next tick.
func (s *Service) applyWorkloadObservation(ctx context.Context, customerID, clusterID, namespace, releaseName string, item *operatorv1.WorkloadIdentityItem, key string) {
	observation, observed := workloadObservationFromItem(item)
	if !observed {
		return
	}
	if err := s.store.Inventories().UpdateWorkloadObservation(ctx, customerID, clusterID, namespace, releaseName, observation); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// The row vanished between the identity apply and this write, or the
			// inventory never had a row for this release. The next tick retries;
			// buffering observations is deliberately out of scope (TASK-168 §4.3).
			s.logger.Debug("dropping workload observation for unknown inventory row", "namespace_release", key)
			return
		}
		s.logger.Warn("failed to persist workload observation", "namespace_release", key, "error", err)
		return
	}
	s.logger.Debug("persisted workload observation", "namespace_release", key,
		"containers", len(observation.Containers), "observed_at", observation.ObservedAt.Format(time.RFC3339))
}

// workloadObservationFromItem projects the report item's observed fields onto
// the store contract. observed_at presence is the gate: an item that never
// carried a projection (old operator) reports the field as absent, and a
// projection must never be fabricated from it. Empty containers / image refs
// and an absent current_replicas are preserved as per-field "not observed"
// sentinels; a present current_replicas of 0 is a real observed zero and is
// carried as a non-nil pointer.
func workloadObservationFromItem(item *operatorv1.WorkloadIdentityItem) (store.WorkloadObservation, bool) {
	if item == nil {
		return store.WorkloadObservation{}, false
	}
	observedAt := item.GetObservedAt()
	if observedAt == nil || !observedAt.IsValid() {
		return store.WorkloadObservation{}, false
	}
	observation := store.WorkloadObservation{
		Containers: append([]string{}, item.GetContainers()...),
		ImageRefs:  make(map[string]string, len(item.GetCurrentImageRefs())),
		ObservedAt: observedAt.AsTime().UTC(),
	}
	for container, ref := range item.GetCurrentImageRefs() {
		observation.ImageRefs[container] = ref
	}
	if item.CurrentReplicas != nil {
		replicas := item.GetCurrentReplicas()
		observation.Replicas = &replicas
	}
	return observation, true
}

// ReplayAfterInventory binds a buffered identity to the inventory row that a
// SyncInventory Upsert just created (REQ-088 D5=A). It is the orchestrator's
// event-driven replay hook and is idempotent by the release key. Best effort:
// nothing buffered (ErrNotFound) or the row still absent are nil (the periodic
// sweep retries); a D4=C conflict is a deterministic terminal state and also
// removes the pending row; only real store failures propagate and keep the
// pending row for the next event or sweep. Runs under the per-release-key lock
// so it can never interleave with a concurrent report buffer/apply for the
// same release (a stale replay must not delete a newer buffered report or
// regress a fresher row bind).
func (s *Service) ReplayAfterInventory(ctx context.Context, customerID, clusterID, namespace, releaseName string) error {
	lock := s.identityStripeLock(customerID, clusterID, namespace, releaseName)
	lock.Lock()
	defer lock.Unlock()
	return s.replayAfterInventoryLocked(ctx, customerID, clusterID, namespace, releaseName)
}

// replayAfterInventoryLocked is the replay body; the caller holds the
// release-key identityStripeLock.
func (s *Service) replayAfterInventoryLocked(ctx context.Context, customerID, clusterID, namespace, releaseName string) error {
	key := reportReleaseKey(namespace, releaseName)
	pending, err := s.store.PendingWorkloadIdentities().GetByReleaseKey(ctx, customerID, clusterID, namespace, releaseName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil // nothing buffered for this release
		}
		return fmt.Errorf("load pending workload identity for replay: %w", err)
	}
	// A pending row that somehow lacks a complete four-tuple (defensive: the
	// buffer path already validates completeness) must never be bound. Treat
	// it as corrupt data: drop it so it cannot linger as an orphan.
	if !identityComplete(store.WorkloadIdentity{
		Kind: pending.WorkloadKind, Name: pending.WorkloadName,
		Namespace: pending.WorkloadNamespace, UID: pending.WorkloadUID,
	}) {
		s.logger.Warn("dropping incomplete pending workload identity", "namespace_release", key)
		if err := s.store.PendingWorkloadIdentities().DeleteByReleaseKey(ctx, customerID, clusterID, namespace, releaseName); err != nil {
			return fmt.Errorf("delete incomplete pending workload identity: %w", err)
		}
		s.countIdentityReportDropped()
		return nil
	}
	row, err := s.store.Inventories().GetByReleaseKey(ctx, customerID, clusterID, namespace, releaseName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil // row still absent (marked missing or synced later); sweep retries
		}
		return fmt.Errorf("load inventory row for identity replay: %w", err)
	}
	report := store.WorkloadIdentity{
		Kind: pending.WorkloadKind, Name: pending.WorkloadName,
		Namespace: pending.WorkloadNamespace, UID: pending.WorkloadUID,
	}
	resolution, err := s.applyIdentity(ctx, customerID, clusterID, namespace, releaseName, rowWorkloadIdentity(row), report, key)
	if err != nil {
		return err
	}
	// Bound, already identical, or deterministically conflicting: the pending
	// row has served its purpose. Only a store failure above would keep it.
	if err := s.store.PendingWorkloadIdentities().DeleteByReleaseKey(ctx, customerID, clusterID, namespace, releaseName); err != nil {
		return fmt.Errorf("delete pending workload identity after replay: %w", err)
	}
	if resolution == WorkloadIdentityBind || resolution == WorkloadIdentityUpdateUID {
		s.logger.Info("replayed buffered workload identity after inventory row appeared", "namespace_release", key)
		s.countIdentityBoundAfterInventory()
	}
	return nil
}

// ReconcilePendingIdentities is the periodic convergence and orphan cleanup
// pass driven by the orchestrator host (REQ-088 D3/D5). It first attempts the
// replay/bind for every buffered identity under the per-release-key lock (rows
// may have appeared since the last sync), then purges the remaining orphans —
// pending rows older than the TTL whose release never materialized — with one
// atomic store PurgeExpired call (single purge implementation).
func (s *Service) ReconcilePendingIdentities(ctx context.Context) error {
	pendings, err := s.store.PendingWorkloadIdentities().ListAll(ctx)
	if err != nil {
		return fmt.Errorf("list pending workload identities for sweep: %w", err)
	}
	for _, pending := range pendings {
		key := reportReleaseKey(pending.Namespace, pending.ReleaseName)
		lock := s.identityStripeLock(pending.CustomerID, pending.ClusterID, pending.Namespace, pending.ReleaseName)
		lock.Lock()
		err := s.replayAfterInventoryLocked(ctx, pending.CustomerID, pending.ClusterID, pending.Namespace, pending.ReleaseName)
		lock.Unlock()
		if err != nil {
			// Transient failure: keep the pending row; the next sweep retries.
			s.logger.Warn("pending workload identity replay failed", "namespace_release", key, "error", err)
			continue
		}
	}
	// Replay handled every bindable row. Anything still pending whose
	// created_at predates the TTL is an orphan (the release never appeared as
	// an inventory row) — one atomic store purge is the single TTL
	// implementation (the store PurgeExpired is exercised by this path).
	cutoff := time.Now().UTC().Add(-s.bufferedIdentityTTL())
	purged, err := s.store.PendingWorkloadIdentities().PurgeExpired(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("purge expired pending workload identities: %w", err)
	}
	if purged > 0 {
		s.logger.Info("purged orphan pending workload identities past TTL",
			"purged", purged, "cutoff", cutoff.Format(time.RFC3339))
		s.countIdentityPendingPurged(purged)
	}
	return nil
}

// pendingIdentityTTL returns the configured buffered-identity TTL (default
// defaultPendingIdentityTTL); tests override it via WithPendingIdentityTTL.
func (s *Service) bufferedIdentityTTL() time.Duration {
	if s.pendingIdentityTTL > 0 {
		return s.pendingIdentityTTL
	}
	return defaultPendingIdentityTTL
}

// rowWorkloadIdentity projects the workload identity columns of an inventory
// row into a store.WorkloadIdentity.
func rowWorkloadIdentity(row *store.ReleaseInventory) store.WorkloadIdentity {
	return store.WorkloadIdentity{
		Kind:      row.WorkloadKind,
		Name:      row.WorkloadName,
		Namespace: row.WorkloadNamespace,
		UID:       row.WorkloadUID,
	}
}

func (s *Service) countIdentityReportBuffered() {
	if s.identityMetrics != nil {
		s.identityMetrics.ReportBuffered.Inc()
	}
}

func (s *Service) countIdentityBoundAfterInventory() {
	if s.identityMetrics != nil {
		s.identityMetrics.BoundAfterInventory.Inc()
	}
}

func (s *Service) countIdentityConflict() {
	if s.identityMetrics != nil {
		s.identityMetrics.Conflict.Inc()
	}
}

func (s *Service) countIdentityReportDropped() {
	if s.identityMetrics != nil {
		s.identityMetrics.ReportDropped.Inc()
	}
}

func (s *Service) countIdentityPendingPurged(count int64) {
	if s.identityMetrics != nil {
		s.identityMetrics.PendingPurged.Add(float64(count))
	}
}

// reportReleaseKey formats the stable release key used by both sides of the
// identity report matching.
func reportReleaseKey(namespace, releaseName string) string {
	return strings.TrimSpace(namespace) + "/" + strings.TrimSpace(releaseName)
}
