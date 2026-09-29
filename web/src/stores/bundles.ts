import { computed, ref } from 'vue';
import { defineStore } from 'pinia';
import {
  getBundle,
  listBundles,
  mapBundleError,
  type BundleDetailView,
  type BundleFailure,
  type BundleFilters,
  type BundleSummaryView,
} from '@/connect/bundle-api';
import { BundleStatus } from '@/gen/common/v1/domain_pb';

/*
 * Bundle catalogue state (A8).
 *
 * Cursors are opaque and bound to the filter that produced them, so ANY filter
 * change restarts from the first page — carrying the token across filters is the
 * mistake the contract explicitly warns about.
 */
export const useBundlesStore = defineStore('bundles', () => {
  const filters = ref<BundleFilters>({ releaseDefinitionId: '', chartNameFilter: '', status: null });
  const bundles = ref<BundleSummaryView[]>([]);
  const nextPageToken = ref('');
  const totalSize = ref(0);
  const loading = ref(false);
  const appending = ref(false);
  const failure = ref<BundleFailure | null>(null);
  const detail = ref<BundleDetailView | null>(null);
  const detailLoading = ref(false);
  const detailFailure = ref<BundleFailure | null>(null);

  /**
   * The definition is REQUIRED by the server (scope + authorization). We do not spend
   * a request to learn that: without it the page shows a prompt instead of a 403 that
   * would bounce the operator to /forbidden.
   */
  const needsDefinition = computed(() => (filters.value.releaseDefinitionId ?? '').trim() === '');
  const hasMore = computed(() => nextPageToken.value !== '');
  // An empty catalogue is only meaningful once a definition has been given.
  const isEmpty = computed(
    () => !needsDefinition.value && !loading.value && !failure.value && bundles.value.length === 0,
  );

  /** Always fetches page 1 with the current filters (used after any filter change). */
  async function fetchFirstPage(): Promise<void> {
    if (needsDefinition.value) {
      bundles.value = [];
      nextPageToken.value = '';
      totalSize.value = 0;
      failure.value = null;
      return;
    }
    loading.value = true;
    try {
      const page = await listBundles(filters.value, '');
      bundles.value = page.bundles;
      nextPageToken.value = page.nextPageToken;
      totalSize.value = page.totalSize;
      failure.value = null;
    } catch (error) {
      failure.value = mapBundleError(error);
      bundles.value = [];
      nextPageToken.value = '';
      totalSize.value = 0;
    } finally {
      loading.value = false;
    }
  }

  async function load(next?: BundleFilters): Promise<void> {
    if (next) filters.value = next;
    detail.value = null;
    detailFailure.value = null;
    await fetchFirstPage();
  }

  async function appendNextPage(): Promise<void> {
    if (!nextPageToken.value || appending.value) return;
    appending.value = true;
    failure.value = null;
    try {
      const page = await listBundles(filters.value, nextPageToken.value);
      bundles.value = [...bundles.value, ...page.bundles];
      nextPageToken.value = page.nextPageToken;
      totalSize.value = page.totalSize;
    } catch (error) {
      const mapped = mapBundleError(error);
      failure.value = mapped;
      // An unusable cursor means starting over, not retrying the same token.
      if (mapped.code === 'invalid_input' || mapped.code === 'stale_cursor') {
        nextPageToken.value = '';
        await fetchFirstPage();
        failure.value = mapped;
      }
    } finally {
      appending.value = false;
    }
  }

  async function openDetail(bundle: BundleSummaryView): Promise<void> {
    // GetBundle is authorized against the definition, so there is nothing to fetch
    // without one.
    if (needsDefinition.value) return;
    detailLoading.value = true;
    detailFailure.value = null;
    detail.value = null;
    try {
      detail.value = await getBundle(bundle.id, filters.value.releaseDefinitionId ?? '');
    } catch (error) {
      detailFailure.value = mapBundleError(error);
    } finally {
      detailLoading.value = false;
    }
  }

  function closeDetail(): void {
    detail.value = null;
    detailFailure.value = null;
  }

  return {
    filters,
    bundles,
    nextPageToken,
    totalSize,
    loading,
    appending,
    failure,
    detail,
    detailLoading,
    detailFailure,
    hasMore,
    isEmpty,
    needsDefinition,
    load,
    appendNextPage,
    openDetail,
    closeDetail,
    statuses: [BundleStatus.RECEIVED, BundleStatus.VALIDATED, BundleStatus.REJECTED, BundleStatus.ARCHIVED],
  };
});
