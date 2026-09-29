<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onMounted } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import RouteRuleEditor from '@/components/clusters/RouteRuleEditor.vue';
import { useScopeNames } from '@/composables/useScopeNames';
import { useClusterStore } from '@/stores/cluster';
import { useAuthStore } from '@/stores/auth';

const route = useRoute();
const router = useRouter();
const store = useClusterStore();
const auth = useAuthStore();
const customerId = String(route.params.customerId);
const clusterId = String(route.params.clusterId);
// Same switch the router uses; the release subtree is optional in a deployment.
const releaseInventoryEnabled = import.meta.env.VITE_ENABLE_RELEASE_INVENTORY !== 'false';
// Plan N7: resolve the CUSTOMER name instead of printing its identifier. The cluster
// is loaded by this page itself (`loadCluster` below), so the composable must not
// resolve it too — that produced a duplicate GetCluster/GetClusterRoutes pair.
const { customerName } = useScopeNames({ resolveCluster: false });
const clusterName = computed(() => store.current?.name ?? clusterId);
const operatorManagementEnabled = import.meta.env.VITE_FEATURE_OPERATOR_MANAGEMENT !== 'false';
const endpoints = {
  cacheEndpoint: import.meta.env.VITE_ARTIFACT_CACHE_ENDPOINT ?? 'cache.local',
  registryEndpoint: import.meta.env.VITE_ARTIFACT_REGISTRY_ENDPOINT ?? 'registry.local',
};

onMounted(() => store.loadCluster(clusterId));

async function handleDisable() {
  if (!window.confirm(t('cluster.detail.disableConfirm'))) return;
  await store.disable(clusterId);
}
</script>

<template>
  <section class="page">
    <LoadingState v-if="store.loading" :message="t('state.loading.cluster')" />
    <ForbiddenState v-else-if="store.forbidden" />
    <ErrorState v-else-if="store.error" :message="store.error">
      <button type="button" @click="router.push({ name: 'ClusterList', params: { customerId } })">{{ t('cluster.detail.back') }}</button>
    </ErrorState>

    <template v-else-if="store.current">
      <header class="page__header">
        <div>
          <p class="eyebrow">{{ t('cluster.detail.label') }}</p>
          <h1>{{ store.current.name }}</h1>
          <p>{{ store.current.enabled ? t('cluster.detail.activeTarget') : t('cluster.detail.inactiveTarget') }}</p>
        </div>
        <div class="actions">
          <!-- W3 (UX plan N8/N10): the whole release subtree was reachable only
               through the inventory table, which itself had no entry from here. -->
          <RouterLink
            v-if="releaseInventoryEnabled"
            :to="{ name: 'ReleaseInventory', params: { customerId, clusterId }, query: { customerName, clusterName } }"
          >{{ t('cluster.detail.releases') }}</RouterLink>
          <RouterLink
            v-if="operatorManagementEnabled"
            :to="{ name: 'OperatorList', params: { customerId, clusterId } }"
          >{{ t('cluster.detail.operators') }}</RouterLink>
          <RouterLink v-if="auth.canWrite" :to="{ name: 'ClusterEdit', params: { customerId, clusterId } }">{{ t('customer.list.edit') }}</RouterLink>
          <button v-if="auth.canWrite && store.current.enabled" type="button" class="danger" @click="handleDisable">{{ t('cluster.detail.disable') }}</button>
        </div>
      </header>

      <dl class="summary">
        <div><dt>{{ t('cluster.detail.version') }}</dt><dd>{{ store.current.version }}</dd></div>
        <div><dt>{{ t('cluster.detail.status') }}</dt><dd>{{ store.current.enabled ? t('status.active') : t('status.disabled') }}</dd></div>
        <div><dt>{{ t('cluster.detail.routingRules') }}</dt><dd>{{ store.current.routeCount }}</dd></div>
      </dl>

      <RouteRuleEditor
        :title="t('cluster.detail.imageRoutes')"
        artifact-type="image"
        :rules="store.current.imageRules"
        :endpoints="endpoints"
        readonly
      />
      <RouteRuleEditor
        :title="t('cluster.detail.chartRoutes')"
        artifact-type="chart"
        :rules="store.current.chartRules"
        :endpoints="endpoints"
        readonly
      />
    </template>
  </section>
</template>

<style scoped>
.page { display: grid; gap: 1.5rem; max-width: 72rem; margin: 0 auto; }
.page__header, .actions { display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
h1, p { margin: 0; }
.eyebrow { color: var(--color-primary); font-weight: 700; text-transform: uppercase; font-size: var(--font-size-xs); }
.page__header p:last-child { color: var(--color-muted); margin-top: 0.375rem; }
.summary { display: grid; grid-template-columns: repeat(3, 1fr); margin: 0; border: 1px solid var(--color-border-strong); border-radius: 0.75rem; }
.summary div { padding: 1rem; }
.summary dt { color: var(--color-muted); }
.summary dd { margin: 0.25rem 0 0; font-weight: 700; }
.actions a, button { padding: 0.5rem 0.75rem; border: 1px solid var(--color-subtle); border-radius: 0.375rem; background: var(--color-surface); }
.danger { color: var(--color-error); }
</style>
