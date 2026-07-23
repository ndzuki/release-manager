<script setup lang="ts">
import { onMounted } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import RouteRuleEditor from '@/components/clusters/RouteRuleEditor.vue';
import { useClusterStore } from '@/stores/cluster';
import { useAuthStore } from '@/stores/auth';

const route = useRoute();
const router = useRouter();
const store = useClusterStore();
const auth = useAuthStore();
const customerId = String(route.params.customerId);
const clusterId = String(route.params.clusterId);
const endpoints = {
  cacheEndpoint: import.meta.env.VITE_ARTIFACT_CACHE_ENDPOINT ?? 'cache.local',
  registryEndpoint: import.meta.env.VITE_ARTIFACT_REGISTRY_ENDPOINT ?? 'registry.local',
};

onMounted(() => store.loadCluster(clusterId));

async function handleDisable() {
  if (!window.confirm('Disabling this cluster removes it from release targets and revokes active operator access. Continue?')) return;
  await store.disable(clusterId);
}
</script>

<template>
  <section class="page">
    <LoadingState v-if="store.loading" message="Loading cluster…" />
    <ForbiddenState v-else-if="store.forbidden" />
    <ErrorState v-else-if="store.error" :message="store.error">
      <button type="button" @click="router.push({ name: 'ClusterList', params: { customerId } })">Back to clusters</button>
    </ErrorState>

    <template v-else-if="store.current">
      <header class="page__header">
        <div>
          <p class="eyebrow">Cluster</p>
          <h1>{{ store.current.name }}</h1>
          <p>{{ store.current.enabled ? 'Active release target' : 'Disabled — unavailable as a release target' }}</p>
        </div>
        <div class="actions">
          <RouterLink v-if="auth.canWrite" :to="{ name: 'ClusterEdit', params: { customerId, clusterId } }">Edit</RouterLink>
          <button v-if="auth.canWrite && store.current.enabled" type="button" class="danger" @click="handleDisable">Disable cluster</button>
        </div>
      </header>

      <dl class="summary">
        <div><dt>Version</dt><dd>{{ store.current.version }}</dd></div>
        <div><dt>Status</dt><dd>{{ store.current.enabled ? 'Active' : 'Disabled' }}</dd></div>
        <div><dt>Routing rules</dt><dd>{{ store.current.routeCount }}</dd></div>
      </dl>

      <RouteRuleEditor
        title="Image routes"
        artifact-type="image"
        :rules="store.current.imageRules"
        :endpoints="endpoints"
        readonly
      />
      <RouteRuleEditor
        title="Chart routes"
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
.eyebrow { color: #2563eb; font-weight: 700; text-transform: uppercase; font-size: 0.75rem; }
.page__header p:last-child { color: #64748b; margin-top: 0.375rem; }
.summary { display: grid; grid-template-columns: repeat(3, 1fr); margin: 0; border: 1px solid #cbd5e1; border-radius: 0.75rem; }
.summary div { padding: 1rem; }
.summary dt { color: #64748b; }
.summary dd { margin: 0.25rem 0 0; font-weight: 700; }
.actions a, button { padding: 0.5rem 0.75rem; border: 1px solid #94a3b8; border-radius: 0.375rem; background: #fff; }
.danger { color: #b91c1c; }
</style>
