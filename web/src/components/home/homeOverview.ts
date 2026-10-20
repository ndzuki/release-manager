import type { NonTerminalOperationItem } from '@/connect/operation-api';

/*
 * Home overview model (TASK-394).
 *
 * The page states four numbers and two health panels, and every one of them is
 * derived from an RPC that already exists. This module is the join between those
 * reads and the rendered copy, kept pure so the counts can be tested without a
 * transport:
 *
 *   - releases / pending convergence / cluster health ← ListReleaseInventory,
 *     the organization-scoped inventory view (one row per observed release).
 *     "Pending convergence" counts a release whose OBSERVED status diverges from
 *     the desired one (`out_of_sync` or `missing`). `unknown` is deliberately not
 *     counted: it means the console could not read the status, and the release
 *     inventory list treats it as "not a statement" (UX-061), so counting it
 *     would be a false positive.
 *   - in-progress operations ← ListNonTerminalOperations, the cross-release feed.
 *     That RPC has NO total, so the card shows the first page's count and the
 *     panel says so when a next page token exists.
 *   - emergency changes needing explicit convergence ← ListStuckLocks: emergency
 *     operations whose cluster effect is still unknown past the observation
 *     window. That is exactly the "needs an explicit convergence decision" set.
 *

 * operation query exists (ListOperations is scoped to one release definition),
 * and the audit query is a cursor-paginated event list with no aggregate. The

 */
export type InventoryReleaseStatus = 'active' | 'missing' | 'out_of_sync' | 'unknown';

export interface InventoryRelease {
  customerId: string;
  customerName: string;
  clusterId: string;
  clusterName: string;
  releaseDefinitionId: string;
  releaseDefinitionName: string;
  namespace: string;
  releaseName: string;
  revision: number;
  status: InventoryReleaseStatus;
}

/** One SUCCESSFUL operator probe; a failed probe is simply absent from the list. */
export interface ClusterProbe {
  clusterId: string;
  online: number;
  heartbeatSeconds: number | null;
}

export interface ClusterRef {
  clusterId: string;
  customerId: string;
  name: string;
}

export interface ClusterHealth extends ClusterRef {
  /** Releases observed in this cluster. */
  total: number;
  /** Of those, the ones whose observed status is `active`. */
  synced: number;
  /** `null` = not probed (beyond the limit, feature off, or the probe failed). */
  onlineOperators: number | null;
  heartbeatSeconds: number | null;
}

export type OperatorCoverage = 'complete' | 'partial' | 'unavailable';

export interface HomeSummary {
  /** Every observed release in the organization's inventory. */
  releaseTotal: number;
  /** `out_of_sync` + `missing` releases. */
  pendingConvergence: number;
  clusters: ClusterHealth[];
  /** Clusters the inventory knows about (the panel renders at most CLUSTER_LIMIT). */
  clustersTotal: number;
  onlineOperators: number;
  probedClusters: number;
  operatorCoverage: OperatorCoverage;
  /** Non-terminal operations already dispatched (queued or running). */
  dispatching: number;
  operations: NonTerminalOperationItem[];
  operationsTruncated: boolean;
  stuckLocks: number;
}

export interface HomeOverviewInput {
  inventory: InventoryRelease[];
  /** False while the inventory read is pending or failed: the live count must not read as 0. */
  inventoryLoaded: boolean;
  operations: NonTerminalOperationItem[];
  operationsTruncated: boolean;
  stuckLocks: number;
  probes: ClusterProbe[];
}

/** Clusters rendered in the health panel, and therefore the ones probed. */
export const CLUSTER_LIMIT = 6;
/** Operation rows rendered in the panel (the card counts the whole loaded page). */
export const OPERATION_ROW_LIMIT = 5;
/** Server maximum page size: one page is the card's count unless it is full. */
export const OPERATION_PAGE_SIZE = 100;

/**
 * Distinct clusters in the inventory, ordered by display name.
 *
 * The order is stable so the probed set is stable too: reloading the page must
 * not move which six clusters get an operator probe.
 */
export function inventoryClusters(inventory: InventoryRelease[]): ClusterRef[] {
  const seen = new Map<string, ClusterRef>();
  for (const release of inventory) {
    if (seen.has(release.clusterId)) continue;
    seen.set(release.clusterId, {
      clusterId: release.clusterId,
      customerId: release.customerId,
      name: release.clusterName || release.clusterId,
    });
  }
  return [...seen.values()].sort(
    (left, right) => left.name.localeCompare(right.name, 'zh-Hans-CN') || left.clusterId.localeCompare(right.clusterId),
  );
}

export function summarizeOverview(input: HomeOverviewInput): HomeSummary {
  const probes = new Map(input.probes.map((probe) => [probe.clusterId, probe]));
  const clusters = inventoryClusters(input.inventory).map<ClusterHealth>((cluster) => {
    const rows = input.inventory.filter((release) => release.clusterId === cluster.clusterId);
    const probe = probes.get(cluster.clusterId);
    return {
      ...cluster,
      total: rows.length,
      synced: rows.filter((release) => release.status === 'active').length,
      onlineOperators: probe ? probe.online : null,
      heartbeatSeconds: probe ? probe.heartbeatSeconds : null,
    };
  });

  const probedClusters = clusters.filter((cluster) => cluster.onlineOperators !== null).length;
  // An empty release inventory says nothing about operators: with zero clusters we never
  // probed one, so the only honest answer is "unavailable". Reporting 'complete' here
  // rendered a positive claim ("0 operators online") the page had no evidence for
  // (independent review of PR #484).
  const coverage: OperatorCoverage =
    clusters.length === 0
      ? 'unavailable'
      : probedClusters === 0
        ? 'unavailable'
        : probedClusters === clusters.length
          ? 'complete'
          : 'partial';

  return {
    releaseTotal: input.inventory.length,
    pendingConvergence: input.inventory.filter(
      (release) => release.status === 'out_of_sync' || release.status === 'missing',
    ).length,
    clusters,
    clustersTotal: clusters.length,
    onlineOperators: clusters.reduce((total, cluster) => total + (cluster.onlineOperators ?? 0), 0),
    probedClusters,
    operatorCoverage: coverage,
    dispatching: input.operations.filter(
      (operation) => operation.state === 'queued' || operation.state === 'running',
    ).length,
    operations: input.operations,
    operationsTruncated: input.operationsTruncated,
    stuckLocks: input.stuckLocks,
  };
}
