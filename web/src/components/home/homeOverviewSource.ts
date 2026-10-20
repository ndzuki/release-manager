import { create } from '@bufbuild/protobuf';
import {
  ListReleaseInventoryRequestSchema,
  ReleaseInventoryStatus,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { orchestratorClient } from '@/connect/client';
import { listOperators } from '@/connect/operator-api';
import { listStuckLocks } from '@/connect/stuck-lock-api';
import type { InventoryRelease, InventoryReleaseStatus } from './homeOverview';

/*
 * The RPC seam behind the home overview (TASK-394).
 *
 * Everything the page renders comes from a read that already exists, and each
 * read is isolated here so the page can be mounted in a test with three fakes
 * instead of a transport. Two notes on WHY the calls are shaped this way:
 *
 *   - ListReleaseInventory has no wrapper in `src/connect/` yet (`listReleases`
 *     is scoped to one customer+cluster and cannot serve an organization-wide
 *     overview). It is called through the generated client that `connect/client`
 *     exports for exactly this purpose — the same pattern `stores/releaseInventory`
 *     uses for ListReleases. Adding a wrapper is a `connect/` change, which is
 *     outside this task's write domain.
 *   - The operator list is scoped to one cluster, so the live count is a bounded
 *     probe: the page probes the clusters it also renders (CLUSTER_LIMIT) and
 *     states its coverage instead of pretending to have counted every cluster.
 *     Filtering to `active` + `online` makes `total_count` the exact number of
 *     online operators rather than the length of one page.
 */

export interface OperatorProbeResult {
  online: number;
  heartbeatSeconds: number | null;
}

export interface HomeOverviewSource {
  listInventory(): Promise<InventoryRelease[]>;
  /** Number of stuck emergency locks in the caller's organization. */
  listStuckLocks(): Promise<number>;
  probeOperators(customerId: string, clusterId: string): Promise<OperatorProbeResult>;
}

function inventoryStatus(value: ReleaseInventoryStatus): InventoryReleaseStatus {
  switch (value) {
    case ReleaseInventoryStatus.ACTIVE:
      return 'active';
    case ReleaseInventoryStatus.MISSING:
      return 'missing';
    case ReleaseInventoryStatus.OUT_OF_SYNC:
      return 'out_of_sync';
    default:
      // UNSPECIFIED (and any value a newer server adds) is not a statement about
      // the release; the inventory page reads it the same way (UX-061).
      return 'unknown';
  }
}

export const defaultHomeOverviewSource: HomeOverviewSource = {
  async listInventory(): Promise<InventoryRelease[]> {
    const response = await orchestratorClient.listReleaseInventory(
      create(ListReleaseInventoryRequestSchema, {}),
    );
    return response.rows.map((row) => ({
      customerId: row.customerId,
      customerName: row.customerName,
      clusterId: row.clusterId,
      clusterName: row.clusterName,
      releaseDefinitionId: row.releaseDefinitionId,
      releaseDefinitionName: row.releaseDefinitionName,
      namespace: row.namespace,
      releaseName: row.releaseName,
      revision: row.revision,
      status: inventoryStatus(row.status),
    }));
  },

  async listStuckLocks(): Promise<number> {
    const locks = await listStuckLocks();
    return locks.length;
  },

  async probeOperators(customerId: string, clusterId: string): Promise<OperatorProbeResult> {
    const page = await listOperators(
      customerId,
      clusterId,
      { lifecycleStatus: 'active', sessionStatus: 'online' },
      '',
    );
    return {
      online: page.totalCount,
      heartbeatSeconds: page.heartbeatIntervalSeconds > 0 ? page.heartbeatIntervalSeconds : null,
    };
  },
};

let source: HomeOverviewSource = defaultHomeOverviewSource;

/** Replaces the source for the rest of the test file that installs it. */
export function setHomeOverviewSourceForTest(next: HomeOverviewSource): void {
  source = next;
}

export function resetHomeOverviewSource(): void {
  source = defaultHomeOverviewSource;
}

export function homeOverviewSource(): HomeOverviewSource {
  return source;
}
