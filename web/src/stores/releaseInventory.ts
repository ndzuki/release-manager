import { Code, ConnectError } from '@connectrpc/connect';
import { defineStore } from 'pinia';
import { computed, ref } from 'vue';
import { create } from '@bufbuild/protobuf';
import type { Timestamp } from '@bufbuild/protobuf/wkt';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import {
  ListReleasesRequestSchema,
  ReleaseInventoryStatus,
  type ReleaseSummary as ProtoReleaseSummary,
  type ListReleasesResponse,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { orchestratorClient } from '@/connect/client';
import { correlationLine, describeError } from '@/connect/error-copy';

export type ReleaseStatus = 'active' | 'missing' | 'out_of_sync' | 'unknown';
/**
 * The statuses the wire accepts as a filter. `unknown` is display-only: UNSPECIFIED
 * means "no filter" to the server (api/proto/orchestrator/v1/orchestrator.proto),
 * so sending it as a filter would return every row as if it had matched.
 */
export type FilterableReleaseStatus = Exclude<ReleaseStatus, 'unknown'>;
export type StatusFilter = FilterableReleaseStatus | undefined;

export interface ReleaseSummary {
  releaseDefinitionId: string;
  namespace: string;
  name: string;
  chart: string;
  chartVersion: string;
  revision: number;
  status: ReleaseStatus;
  valuesDigest: string;
  lastSyncAt: string | null;
  // Optional emergency/convergence summary fields (REQ-079 D5): consumed once
  // from ListReleases — never a per-row RPC (AC-058-08).
  emergencyConflict: boolean;
  pendingConvergenceCount: number;
  revertStatusSummary: string;
}

export interface ReleaseInventoryState {
  releases: ReleaseSummary[];
  nextCursor: string | null;
  totalCount: number;
  statusFilter: StatusFilter;
  nameSearch: string;
  loading: boolean;
  appending: boolean;
  error: string | null;
  syncRequestId: string | null;
  syncing: boolean;
  syncError: string | null;
  lastLoadedAt: number | null;
  customerId: string | null;
  clusterId: string | null;
}

const statusToProto: Record<FilterableReleaseStatus, ReleaseInventoryStatus> = {
  active: ReleaseInventoryStatus.ACTIVE,
  missing: ReleaseInventoryStatus.MISSING,
  out_of_sync: ReleaseInventoryStatus.OUT_OF_SYNC,
};

function statusFromProto(status: ReleaseInventoryStatus): ReleaseStatus {
  switch (status) {
    case ReleaseInventoryStatus.ACTIVE:
      return 'active';
    case ReleaseInventoryStatus.MISSING:
      return 'missing';
    case ReleaseInventoryStatus.OUT_OF_SYNC:
      return 'out_of_sync';
    default:
      // UX-061: UNSPECIFIED — which proto3 also produces for a field the server left
      // out — and any enum value a newer server added say nothing about the release.
      // Reading them as Active was the false positive this mapping used to ship.
      return 'unknown';
  }
}

function timestampToISO(timestamp: Timestamp | undefined): string | null {
	return timestamp ? timestampDate(timestamp).toISOString() : null;
}

function mapRelease(release: ProtoReleaseSummary): ReleaseSummary {
	return {
		releaseDefinitionId: release.releaseDefinitionId,
		namespace: release.namespace,
		name: release.name,
		chart: release.chart,
		chartVersion: release.chartVersion,
		revision: release.revision,
		status: statusFromProto(release.status),
		valuesDigest: release.valuesDigest,
		lastSyncAt: timestampToISO(release.lastSyncAt),
		emergencyConflict: release.emergencyConflict,
		pendingConvergenceCount: release.pendingConvergenceCount,
		revertStatusSummary: release.revertStatusSummary,
	};
}

function errorReason(error: unknown): string {
  const connectError = ConnectError.from(error);
  if (connectError.code === Code.InvalidArgument && connectError.metadata.get('X-Reason-Code') === 'invalid_cursor') {
    return '数据已变更，已刷新';
  }
  if (connectError.code === Code.Unavailable && connectError.metadata.get('X-Reason-Code') === 'operator_offline') {
    return 'Operator 离线，无法触发同步';
  }
  if (connectError.code === Code.AlreadyExists && connectError.metadata.get('X-Reason-Code') === 'sync_in_progress') {
    return `同步进行中（请求 ID: ${connectError.metadata.get('X-Sync-Request-ID') ?? 'unknown'}）`;
  }
  if (connectError.code === Code.PermissionDenied) return '无权操作此资源';
  if (connectError.code === Code.NotFound) return '集群或客户不存在';
  return connectError.code === Code.Unavailable ? '网络错误，请检查连接后重试' : connectError.rawMessage;
}

export const useReleaseInventoryStore = defineStore('releaseInventory', () => {
  const releases = ref<ReleaseSummary[]>([]);
  const nextCursor = ref<string | null>(null);
  const totalCount = ref(0);
  const statusFilter = ref<StatusFilter>();
  const nameSearch = ref('');
  const loading = ref(false);
  const appending = ref(false);
  const error = ref<string | null>(null);
  // UX-015 (TASK-325 W1 walkthrough): the read failure copy dropped the
  // correlation line, so a user reporting "the release list is broken" had no
  // requestId to hand over. ErrorState already renders `details`.
  const errorDetails = ref<string | null>(null);
  const syncRequestId = ref<string | null>(null);
  const syncing = ref(false);
  const syncError = ref<string | null>(null);
  const syncErrorDetails = ref<string | null>(null);
  const lastLoadedAt = ref<number | null>(null);
  const customerId = ref<string | null>(null);
  const clusterId = ref<string | null>(null);

  const hasMore = computed(() => nextCursor.value !== null && nextCursor.value !== '');
  const isEmpty = computed(() => !loading.value && !error.value && releases.value.length === 0);

  function resetCache(): void {
    releases.value = [];
    nextCursor.value = null;
    totalCount.value = 0;
    lastLoadedAt.value = null;
    error.value = null;
    errorDetails.value = null;
    syncError.value = null;
    syncErrorDetails.value = null;
  }

  function setScope(nextCustomerId: string, nextClusterId: string): void {
    if (customerId.value === nextCustomerId && clusterId.value === nextClusterId) return;
    customerId.value = nextCustomerId;
    clusterId.value = nextClusterId;
    resetCache();
    // The in-flight refresh belonged to the previous scope; do not let the new
    // scope join it (the old response is allowed to finish, this only unblocks
    // a fresh request for the new scope).
    refreshInFlight = null;
  }

  async function load(options: { append?: boolean } = {}): Promise<void> {
    if (!customerId.value || !clusterId.value) return;
    const append = options.append === true;
    if (append && !hasMore.value) return;
    if (append) appending.value = true;
    else loading.value = true;
    error.value = null;
    errorDetails.value = null;
    try {
      const response = await orchestratorClient.listReleases(create(ListReleasesRequestSchema, {
        customerId: customerId.value,
        clusterId: clusterId.value,
        statusFilter: statusFilter.value ? statusToProto[statusFilter.value] : ReleaseInventoryStatus.UNSPECIFIED,
        nameSearch: nameSearch.value,
        pageSize: 50,
        cursor: append ? nextCursor.value ?? '' : '',
      }));
      applyResponse(response, append);
      lastLoadedAt.value = Date.now();
    } catch (requestError) {
      const reason = errorReason(requestError);
      if (reason === '数据已变更，已刷新' && append) {
        nextCursor.value = null;
        await load();
        return;
      }
      error.value = reason;
      errorDetails.value = correlationLine(describeError(requestError), 'ListReleases');
    } finally {
      loading.value = false;
      appending.value = false;
    }
  }

  function applyResponse(response: ListReleasesResponse, append: boolean): void {
    const mapped = response.releases.map(mapRelease);
    releases.value = append ? [...releases.value, ...mapped] : mapped;
    nextCursor.value = response.nextCursor || null;
    totalCount.value = response.totalCount;
  }

  /**
   * UX-057 (W16): refresh is reachable from four controls (header button, retry
   * notice, empty state, error state) plus the 5-minute cache re-arm, and the
   * in-flight state was never shared. A double click issued two ListReleases
   * calls whose responses could land out of order. Concurrent refreshes now
   * share one request; a deliberate filter change still goes through load().
   */
  let refreshInFlight: Promise<void> | null = null;

  function refresh(): Promise<void> {
    if (refreshInFlight) return refreshInFlight;
    nextCursor.value = null;
    const run = load().finally(() => {
      if (refreshInFlight === run) refreshInFlight = null;
    });
    refreshInFlight = run;
    return run;
  }

  async function triggerSync(): Promise<void> {
    if (!customerId.value || !clusterId.value || syncing.value) return;
    syncing.value = true;
    syncError.value = null;
    syncErrorDetails.value = null;
    try {
      const response = await orchestratorClient.triggerInventorySync({
        customerId: customerId.value,
        clusterId: clusterId.value,
      });
      syncRequestId.value = response.syncRequestId;
    } catch (requestError) {
      const connectError = ConnectError.from(requestError);
      if (connectError.code === Code.AlreadyExists) {
        syncRequestId.value = connectError.metadata.get('X-Sync-Request-ID');
      }
      syncError.value = errorReason(requestError);
      syncErrorDetails.value = correlationLine(describeError(requestError), 'TriggerInventorySync');
    } finally {
      syncing.value = false;
    }
  }

  function setStatusFilter(next: StatusFilter): void {
    statusFilter.value = next;
    nextCursor.value = null;
  }

  function setNameSearch(next: string): boolean {
    if (next.length > 253) return false;
    nameSearch.value = next;
    nextCursor.value = null;
    return true;
  }

  return {
    releases,
    nextCursor,
    totalCount,
    statusFilter,
    nameSearch,
    loading,
    appending,
    error,
    errorDetails,
    syncRequestId,
    syncing,
    syncError,
    syncErrorDetails,
    lastLoadedAt,
    customerId,
    clusterId,
    hasMore,
    isEmpty,
    resetCache,
    setScope,
    load,
    refresh,
    triggerSync,
    setStatusFilter,
    setNameSearch,
  };
});
