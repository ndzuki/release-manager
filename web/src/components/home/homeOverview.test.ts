import { describe, expect, it } from 'vitest';
import type { NonTerminalOperationItem } from '@/connect/operation-api';
import {
  CLUSTER_LIMIT,
  inventoryClusters,
  summarizeOverview,
  type ClusterProbe,
  type InventoryRelease,
} from './homeOverview';

/*
 * The overview numbers are derived from three independent reads, so these cases
 * pin the DERIVATION rather than the rendering: which inventory status counts as
 * pending convergence, how coverage is reported when only some clusters were
 * probed, and that a truncated operation page is surfaced instead of being read
 * as a total.
 */
function release(overrides: Partial<InventoryRelease> = {}): InventoryRelease {
  return {
    customerId: 'cust-1',
    customerName: 'Acme',
    clusterId: 'cluster-1',
    clusterName: 'prod-a',
    releaseDefinitionId: 'def-1',
    releaseDefinitionName: 'checkout',
    namespace: 'apps',
    releaseName: 'checkout',
    revision: 3,
    status: 'active',
    ...overrides,
  };
}

function operation(overrides: Partial<NonTerminalOperationItem> = {}): NonTerminalOperationItem {
  return {
    operationId: 'op-1',
    operationType: 'UPGRADE',
    state: 'running',
    releaseDefinitionId: 'def-1',
    releaseDefinitionName: 'checkout',
    customerId: 'cust-1',
    customerName: 'Acme',
    clusterId: 'cluster-1',
    createdAt: '2026-10-01T00:00:00.000Z',
    updatedAt: null,
    emergency: false,
    revision: 3,
    ...overrides,
  };
}

function probe(clusterId: string, online: number, heartbeatSeconds: number | null = 30): ClusterProbe {
  return { clusterId, online, heartbeatSeconds };
}

function summarize(input: {
  inventory?: InventoryRelease[];
  operations?: NonTerminalOperationItem[];
  operationsTruncated?: boolean;
  stuckLocks?: number;
  probes?: ClusterProbe[];
  inventoryLoaded?: boolean;
}) {
  return summarizeOverview({
    inventory: input.inventory ?? [],
    inventoryLoaded: input.inventoryLoaded ?? true,
    operations: input.operations ?? [],
    operationsTruncated: input.operationsTruncated ?? false,
    stuckLocks: input.stuckLocks ?? 0,
    probes: input.probes ?? [],
  });
}

describe('inventoryClusters', () => {
  it('deduplicates by cluster id and orders by display name', () => {
    const clusters = inventoryClusters([
      release({ clusterId: 'c-b', clusterName: 'prod-b' }),
      release({ clusterId: 'c-a', clusterName: 'prod-a' }),
      release({ clusterId: 'c-b', clusterName: 'prod-b', releaseDefinitionId: 'def-2' }),
    ]);

    expect(clusters.map((cluster) => cluster.clusterId)).toEqual(['c-a', 'c-b']);
    expect(clusters.map((cluster) => cluster.customerId)).toEqual(['cust-1', 'cust-1']);
  });

  // A row whose cluster name did not resolve must still produce one cluster, named
  // by its id — dropping it would hide the releases counted under it.
  it('falls back to the cluster id when the name is missing', () => {
    const [cluster] = inventoryClusters([release({ clusterId: 'cluster-x', clusterName: '' })]);

    expect(cluster?.name).toBe('cluster-x');
  });
});

describe('summarizeOverview', () => {
  it('counts releases that still need convergence and ignores unknown status', () => {
    const summary = summarize({
      inventory: [
        release({ releaseDefinitionId: 'd-1', status: 'active' }),
        release({ releaseDefinitionId: 'd-2', status: 'out_of_sync' }),
        release({ releaseDefinitionId: 'd-3', status: 'missing' }),
        // UNSPECIFIED: the console could not read the status, which is not a claim
        // that the release is drifting (UX-061).
        release({ releaseDefinitionId: 'd-4', status: 'unknown' }),
      ],
    });

    expect(summary.releaseTotal).toBe(4);
    expect(summary.pendingConvergence).toBe(2);
  });

  it('splits per-cluster sync counts from the same inventory read', () => {
    const summary = summarize({
      inventory: [
        release({ clusterId: 'c-a', clusterName: 'prod-a', status: 'active' }),
        release({ clusterId: 'c-a', clusterName: 'prod-a', releaseDefinitionId: 'd-2', status: 'out_of_sync' }),
        release({ clusterId: 'c-b', clusterName: 'prod-b', status: 'active' }),
      ],
      probes: [probe('c-a', 2), probe('c-b', 0)],
    });

    expect(summary.clusters).toHaveLength(2);
    expect(summary.clusters[0]).toMatchObject({ clusterId: 'c-a', name: 'prod-a', synced: 1, total: 2 });
    expect(summary.clusters[1]).toMatchObject({ clusterId: 'c-b', name: 'prod-b', synced: 1, total: 1 });
    expect(summary.onlineOperators).toBe(2);
    expect(summary.operatorCoverage).toBe('complete');
  });

  // The card must not read "0 在线" when the probe never answered: null is
  // "unknown", and the coverage field is what the header reports.
  it('reports a failed probe as unknown rather than zero', () => {
    const summary = summarize({
      inventory: [
        release({ clusterId: 'c-a', clusterName: 'prod-a' }),
        release({ clusterId: 'c-b', clusterName: 'prod-b', releaseDefinitionId: 'd-2' }),
      ],
      probes: [probe('c-a', 1)],
    });

    expect(summary.clusters[1]?.onlineOperators).toBeNull();
    expect(summary.onlineOperators).toBe(1);
    expect(summary.probedClusters).toBe(1);
    expect(summary.operatorCoverage).toBe('partial');
  });

  it('reports coverage as unavailable when no probe succeeded', () => {
    const summary = summarize({ inventory: [release()], probes: [] });

    expect(summary.operatorCoverage).toBe('unavailable');
    expect(summary.clusters[0]?.onlineOperators).toBeNull();
  });

  // An organization with no clusters is not a failed probe: zero operators online
  // is the truth, so the header may say so.
  it('treats an empty inventory as complete coverage', () => {
    expect(summarize({ inventory: [] }).operatorCoverage).toBe('complete');
    expect(summarize({ inventory: [], inventoryLoaded: false }).operatorCoverage).toBe('unavailable');
  });

  it('counts only queued and running operations as dispatched', () => {
    const summary = summarize({
      operations: [
        operation({ operationId: 'op-1', state: 'running' }),
        operation({ operationId: 'op-2', state: 'queued' }),
        operation({ operationId: 'op-3', state: 'preflight' }),
        operation({ operationId: 'op-4', state: 'pending' }),
      ],
    });

    expect(summary.operations).toHaveLength(4);
    expect(summary.dispatching).toBe(2);
  });

  it('carries the truncation flag so a full page is never read as a total', () => {
    expect(summarize({ operations: [operation()], operationsTruncated: true }).operationsTruncated).toBe(true);
  });

  // The panel renders CLUSTER_LIMIT rows and reports the remainder; summarize must
  // therefore keep every cluster and the full total rather than pre-truncating.
  it('keeps every cluster so the panel can report how many it hid', () => {
    const inventory = Array.from({ length: CLUSTER_LIMIT + 2 }, (_, index) =>
      release({ clusterId: `c-${index}`, clusterName: `prod-${index}` }),
    );

    const summary = summarize({ inventory });
    expect(summary.clustersTotal).toBe(CLUSTER_LIMIT + 2);
    expect(summary.clusters).toHaveLength(CLUSTER_LIMIT + 2);
  });

  it('passes the stuck-lock count through', () => {
    expect(summarize({ stuckLocks: 3 }).stuckLocks).toBe(3);
  });
});
