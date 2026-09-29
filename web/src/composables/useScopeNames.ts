import { computed, watchEffect } from 'vue';
import { useRoute } from 'vue-router';
import { useClusterStore } from '@/stores/cluster';
import { useCustomerStore } from '@/stores/customers';

/*
 * Display names for the customer/cluster scope of the current route (plan N7).
 *
 * In-app navigation always carries `customerName`/`clusterName` in the query, but a
 * direct URL (bookmark, paste, refresh after a redirect) does not — and the old
 * fallback then rendered the raw UUID as a breadcrumb level. This resolves the name
 * from the API only when the query does not provide it, so the common path stays
 * request-free.
 */
export interface ScopeNameOptions {
  /**
   * Set false when the page loads the cluster itself (ClusterDetailPage does) — the
   * composable then only reads the store, which avoids a second GetCluster round trip.
   */
  resolveCluster?: boolean;
  resolveCustomer?: boolean;
}

export function useScopeNames(options: ScopeNameOptions = {}) {
  const resolveCluster = options.resolveCluster ?? true;
  const resolveCustomer = options.resolveCustomer ?? true;
  const route = useRoute();
  const customers = useCustomerStore();
  const clusters = useClusterStore();

  const customerId = computed(() => String(route.params.customerId ?? ''));
  const clusterId = computed(() => String(route.params.clusterId ?? ''));
  const queryCustomerName = computed(() =>
    typeof route.query.customerName === 'string' ? route.query.customerName : '',
  );
  const queryClusterName = computed(() =>
    typeof route.query.clusterName === 'string' ? route.query.clusterName : '',
  );

  // A resolved name beats the identifier; the identifier remains the last resort so a
  // failed lookup degrades to what we know rather than to an empty crumb.
  const customerName = computed(
    () => queryCustomerName.value || customers.current?.name || customerId.value,
  );
  const clusterName = computed(
    () => queryClusterName.value || clusters.current?.name || clusterId.value,
  );

  watchEffect(() => {
    if (!resolveCustomer || queryCustomerName.value || !customerId.value) return;
    if (customers.current?.id === customerId.value) return;
    void customers.loadCustomer(customerId.value).catch(() => {
      // A calm failure is fine here: the crumb falls back to the identifier.
    });
  });

  watchEffect(() => {
    if (!resolveCluster || queryClusterName.value || !clusterId.value) return;
    if (clusters.current?.id === clusterId.value) return;
    void clusters.loadCluster(clusterId.value).catch(() => {
      // Same as above.
    });
  });

  return { customerId, clusterId, customerName, clusterName };
}
