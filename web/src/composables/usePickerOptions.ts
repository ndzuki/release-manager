import { ref } from 'vue';
import { t } from '@/i18n/messages';
import { listClusters } from '@/connect/cluster-api';
import { listCustomers } from '@/connect/customer-api';
import { listDefinitions, type DefinitionView } from '@/connect/definition-api';
import type { ClusterSummary } from '@/types/cluster';
import type { CustomerSummary } from '@/types/customer';

/*
 * Lookup lists for the "pick, don't type" pickers (TASK-394 follow-up).
 *
 * Release definitions, bundles and the definition filters all reference customers,
 * clusters and definitions by opaque id. Typing those ids by hand was the single
 * most error-prone step in the console, so every surface that used to ask for one
 * now offers the list instead. The loaders live here (not in a store) because the
 * data is read-only reference data: two pickers on one page must not fight over a
 * single Pinia instance's `clusters` array.
 *
 * Loaders are injectable so a component test can drive the picker without a Connect
 * client; production callers use the defaults.
 */
export interface PickerLoaders {
  customers: (includeDisabled: boolean) => Promise<CustomerSummary[]>;
  clusters: (customerId: string) => Promise<ClusterSummary[]>;
  definitions: () => Promise<DefinitionView[]>;
}

const defaultLoaders: PickerLoaders = {
  customers: (includeDisabled) => listCustomers(includeDisabled),
  clusters: (customerId) => listClusters(customerId),
  definitions: () => listDefinitions({ includeDisabled: true }),
};

export function usePickerOptions(loaders: Partial<PickerLoaders> = {}) {
  const resolved: PickerLoaders = { ...defaultLoaders, ...loaders };
  const customers = ref<CustomerSummary[]>([]);
  const clusters = ref<ClusterSummary[]>([]);
  const definitions = ref<DefinitionView[]>([]);
  const loadingCustomers = ref(false);
  const loadingClusters = ref(false);
  const loadingDefinitions = ref(false);
  const customersError = ref<string | null>(null);
  const clustersError = ref<string | null>(null);
  const definitionsError = ref<string | null>(null);

  // Clusters are per customer; caching avoids a round trip when the user flips back
  // and forth between two customers.
  const clusterCache = new Map<string, ClusterSummary[]>();
  // An EMPTY list is a valid result; a length check would reload it on every open.
  let customersLoaded = false;
  let definitionsLoaded = false;
  // Monotonic request id for the cluster list. A slow response for a customer the user
  // already left must not overwrite the list of the customer they are looking at.
  let clusterRequest = 0;

  async function loadCustomers(includeDisabled = true): Promise<void> {
    if (customersLoaded || loadingCustomers.value) return;
    loadingCustomers.value = true;
    customersError.value = null;
    try {
      customers.value = await resolved.customers(includeDisabled);
      customersLoaded = true;
    } catch {
      customersError.value = t('picker.customersError');
    } finally {
      loadingCustomers.value = false;
    }
  }

  async function reloadCustomers(includeDisabled = true): Promise<void> {
    customers.value = [];
    customersLoaded = false;
    await loadCustomers(includeDisabled);
  }

  async function loadClusters(customerId: string): Promise<void> {
    const scope = customerId.trim();
    // Every call supersedes the previous one, including the cache hit and the empty
    // scope: an in-flight response for the old customer becomes stale by definition.
    const request = ++clusterRequest;
    if (scope === '') {
      clusters.value = [];
      loadingClusters.value = false;
      clustersError.value = null;
      return;
    }
    const cached = clusterCache.get(scope);
    if (cached) {
      clusters.value = cached;
      loadingClusters.value = false;
      clustersError.value = null;
      return;
    }
    loadingClusters.value = true;
    clustersError.value = null;
    try {
      const loaded = await resolved.clusters(scope);
      if (request !== clusterRequest) return;
      clusterCache.set(scope, loaded);
      clusters.value = loaded;
    } catch {
      if (request !== clusterRequest) return;
      clusters.value = [];
      clustersError.value = t('picker.clustersError');
    } finally {
      if (request === clusterRequest) loadingClusters.value = false;
    }
  }

  function clearClusters(): void {
    // Clearing is an intent like any other selection: an in-flight response must not
    // repopulate a list the caller just emptied.
    clusterRequest += 1;
    clusters.value = [];
    clustersError.value = null;
    loadingClusters.value = false;
  }

  async function reloadDefinitions(): Promise<void> {
    definitions.value = [];
    definitionsLoaded = false;
    await loadDefinitions();
  }

  async function loadDefinitions(): Promise<void> {
    if (definitionsLoaded || loadingDefinitions.value) return;
    loadingDefinitions.value = true;
    definitionsError.value = null;
    try {
      definitions.value = await resolved.definitions();
      definitionsLoaded = true;
    } catch {
      definitionsError.value = t('picker.definitionsError');
    } finally {
      loadingDefinitions.value = false;
    }
  }


  return {
    customers,
    clusters,
    definitions,
    loadingCustomers,
    loadingClusters,
    loadingDefinitions,
    customersError,
    clustersError,
    definitionsError,
    loadCustomers,
    reloadCustomers,
    loadClusters,
    clearClusters,
    loadDefinitions,
    reloadDefinitions,
  };
}
