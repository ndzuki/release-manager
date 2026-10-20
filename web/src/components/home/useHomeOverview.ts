import { computed, ref } from 'vue';
import { useOperationFeed } from '@/composables/useOperationFeed';
import {
  CLUSTER_LIMIT,
  OPERATION_PAGE_SIZE,
  inventoryClusters,
  summarizeOverview,
  type ClusterProbe,
  type HomeSummary,
  type InventoryRelease,
} from './homeOverview';
import { homeOverviewSource } from './homeOverviewSource';

/*
 * Home overview loader (TASK-394).
 *
 * Three independent reads feed the page and each one may fail on its own, so the
 * state is per source rather than one page-level error: a refused inventory read
 * must not blank the operation queue, and a maintenance window that only refuses
 * ListNonTerminalOperations must leave the release counts on screen.
 *
 * Source states:
 *   `off`     the feature switch is off — the card shows "—" / 功能未启用
 *   `loading` first read in flight — "—" / 正在加载…
 *   `ok`      the number on the card is real
 *   `error`   the read failed — "—" / 加载失败, with a retry on the panel
 *
 * The operation feed keeps its own richer state machine (maintenance / forbidden
 * are not retryable, see useOperationFeed) and is reused rather than duplicated.
 */
export type SourceState = 'loading' | 'ok' | 'error' | 'off';
export type OperationsState = SourceState | 'maintenance' | 'forbidden';

export interface HomeFeatureFlags {
  inventory: boolean;
  operations: boolean;
  operators: boolean;
}

/**
 * The switches the rest of the console already honours for these surfaces
 * (router feature entries + AppShell). Reading them here keeps the page from
 * calling an RPC whose surface is disabled in this build.
 */
export function defaultHomeFeatures(): HomeFeatureFlags {
  return {
    inventory: import.meta.env.VITE_ENABLE_RELEASE_INVENTORY !== 'false',
    operations: import.meta.env.VITE_ENABLE_RELEASE_OPERATIONS !== 'false',
    operators: import.meta.env.VITE_FEATURE_OPERATOR_MANAGEMENT !== 'false',
  };
}

export function useHomeOverview(features: HomeFeatureFlags = defaultHomeFeatures()) {
  const source = homeOverviewSource();

  const inventory = ref<InventoryRelease[]>([]);
  const inventoryState = ref<SourceState>(features.inventory ? 'loading' : 'off');
  const stuckLocks = ref(0);
  const stuckLocksState = ref<SourceState>(features.operations ? 'loading' : 'off');
  const probes = ref<ClusterProbe[]>([]);
  const started = ref(false);

  const feed = useOperationFeed({ pageSize: OPERATION_PAGE_SIZE });

  /*
   * Only the newest reload may apply its result. A retry clicked while the first
   * read is still in flight would otherwise let the older response overwrite the
   * newer one, which is the stale-window bug the release inventory store fixed
   * (UX-057) — the same guard is cheap here.
   */
  let generation = 0;

  const summary = computed<HomeSummary>(() =>
    summarizeOverview({
      inventory: inventory.value,
      inventoryLoaded: inventoryState.value === 'ok',
      operations: feed.items.value,
      operationsTruncated: feed.nextPageToken.value !== '',
      stuckLocks: stuckLocks.value,
      probes: probes.value,
    }),
  );

  const operationsState = computed<OperationsState>(() => {
    if (!features.operations) return 'off';
    if (feed.maintenance.value) return 'maintenance';
    if (feed.forbidden.value) return 'forbidden';
    if (feed.error.value) return 'error';
    // A load that never started is still "loading": the first paint must not claim
    // an empty queue before the first request has been sent.
    if (feed.loading.value || !started.value) return 'loading';
    return 'ok';
  });

  async function loadInventory(run: number): Promise<void> {
    if (!features.inventory) return;
    inventoryState.value = 'loading';
    try {
      const releases = await source.listInventory();
      if (run !== generation) return;
      inventory.value = releases;
      inventoryState.value = 'ok';
    } catch {
      if (run !== generation) return;
      inventory.value = [];
      inventoryState.value = 'error';
    }
  }

  async function loadStuckLocks(run: number): Promise<void> {
    if (!features.operations) return;
    stuckLocksState.value = 'loading';
    try {
      const count = await source.listStuckLocks();
      if (run !== generation) return;
      stuckLocks.value = count;
      stuckLocksState.value = 'ok';
    } catch {
      if (run !== generation) return;
      stuckLocks.value = 0;
      stuckLocksState.value = 'error';
    }
  }

  /*
   * One probe per rendered cluster, in parallel, each failure isolated: a cluster
   * whose operator list is refused shows "—" for its operator count instead of
   * making the whole health panel look down.
   */
  async function loadProbes(run: number): Promise<void> {
    if (!features.operators) return;
    const targets = inventoryClusters(inventory.value).slice(0, CLUSTER_LIMIT);
    const results = await Promise.all(
      targets.map(async (cluster) => {
        try {
          const probe = await source.probeOperators(cluster.customerId, cluster.clusterId);
          return { clusterId: cluster.clusterId, ...probe } satisfies ClusterProbe;
        } catch {
          return null;
        }
      }),
    );
    if (run !== generation) return;
    probes.value = results.filter((probe): probe is ClusterProbe => probe !== null);
  }

  async function reload(): Promise<void> {
    const run = ++generation;
    started.value = true;
    await Promise.all([
      loadInventory(run),
      features.operations ? feed.load() : Promise.resolve(),
      loadStuckLocks(run),
    ]);
    await loadProbes(run);
  }

  return { summary, inventory, inventoryState, stuckLocksState, operationsState, probes, feed, started, reload };
}
