<script setup lang="ts">
import { computed, onMounted } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import RouteRuleEditor from '@/components/clusters/RouteRuleEditor.vue';
import { useClusterStore } from '@/stores/cluster';
import type { ArtifactType, RouteRuleInput } from '@/types/cluster';
import { useAuthStore } from '@/stores/auth';

const route = useRoute();
const router = useRouter();
const store = useClusterStore();
const auth = useAuthStore();
const customerId = String(route.params.customerId);
const clusterId = route.params.clusterId ? String(route.params.clusterId) : undefined;
const isCreate = computed(() => !clusterId);
const endpoints = {
  cacheEndpoint: import.meta.env.VITE_ARTIFACT_CACHE_ENDPOINT ?? 'cache.local',
  registryEndpoint: import.meta.env.VITE_ARTIFACT_REGISTRY_ENDPOINT ?? 'registry.local',
};
onMounted(async () => {
  if (!auth.canWrite) {
    await router.replace({ name: 'ClusterList', params: { customerId } });
    return;
  }
  if (clusterId) await store.loadCluster(clusterId);
  else store.startCreate();
});

function addRule(artifactType: ArtifactType) {
  if (!store.draft) return;
  const rule: RouteRuleInput = {
    clientKey: crypto.randomUUID(),
    artifactType,
    mode: 'direct',
    sourcePrefix: '',
    targetPrefix: '',
  };
  if (artifactType === 'image') store.draft.imageRules.push(rule);
  else store.draft.chartRules.push(rule);
}

function removeRule(artifactType: ArtifactType, index: number) {
  if (!store.draft) return;
  if (artifactType === 'image') store.draft.imageRules.splice(index, 1);
  else store.draft.chartRules.splice(index, 1);
}

async function handleSave() {
  const saved = await store.save(customerId, clusterId);
  if (saved) await router.push({ name: 'ClusterDetail', params: { customerId, clusterId: saved.id } });
}
</script>

<template>
  <section class="page">
    <LoadingState v-if="store.loading" message="Loading cluster…" />
    <ForbiddenState v-else-if="store.forbidden" />
    <ErrorState v-else-if="store.notFound" message="Cluster not found">
      <RouterLink :to="{ name: 'ClusterList', params: { customerId } }">Back to clusters</RouterLink>
    </ErrorState>

    <form v-else-if="store.draft" class="cluster-form" @submit.prevent="handleSave">
      <header class="page__header">
        <div>
          <p class="eyebrow">{{ isCreate ? 'New cluster' : 'Edit cluster' }}</p>
          <h1>{{ isCreate ? 'Create cluster' : store.current?.name }}</h1>
          <p>Registry credentials are intentionally not accepted or stored.</p>
        </div>
        <div class="actions">
          <RouterLink :to="clusterId ? { name: 'ClusterDetail', params: { customerId, clusterId } } : { name: 'ClusterList', params: { customerId } }">Cancel</RouterLink>
          <button type="submit" class="primary" :disabled="store.saving">
            {{ store.saving ? 'Saving…' : 'Save cluster' }}
          </button>
        </div>
      </header>

      <div v-if="store.saveError" class="save-error" role="alert">
        <strong>{{ store.saveError.code === 'optimistic_lock_conflict' ? 'Data was modified by another user.' : store.saveError.message }}</strong>
        <span v-if="store.saveError.code === 'optimistic_lock_conflict'">Your draft is preserved. Refresh to compare with the server version.</span>
        <button v-if="store.saveError.code === 'network_error'" type="submit">Retry save</button>
        <button v-if="store.saveError.code === 'optimistic_lock_conflict' && clusterId" type="button" @click="store.refreshCluster(clusterId)">Refresh server version</button>
      </div>

      <section class="cluster-fields">
        <label>
          Cluster name
          <input v-model="store.draft.name" maxlength="254" :aria-invalid="Boolean(store.saveError?.fieldViolations?.some((item) => item.field === 'name'))" />
          <small v-for="error in store.saveError?.fieldViolations?.filter((item) => item.field === 'name')" :key="error.description" class="field-error">{{ error.description }}</small>
        </label>
        <label class="checkbox">
          <input v-model="store.draft.enabled" type="checkbox" />
          Enabled as release target
        </label>
      </section>

      <RouteRuleEditor
        title="Image routes"
        artifact-type="image"
        :rules="store.draft.imageRules"
        :violations="store.saveError?.fieldViolations"
        :conflicting-rule-id="store.saveError?.conflictingRuleId"
        :endpoints="endpoints"
        @add="addRule('image')"
        @remove="removeRule('image', $event)"
      />
      <RouteRuleEditor
        title="Chart routes"
        artifact-type="chart"
        :rules="store.draft.chartRules"
        :violations="store.saveError?.fieldViolations"
        :conflicting-rule-id="store.saveError?.conflictingRuleId"
        :endpoints="endpoints"
        @add="addRule('chart')"
        @remove="removeRule('chart', $event)"
      />
    </form>
  </section>
</template>

<style scoped>
.page, .cluster-form { display: grid; gap: 1.5rem; max-width: 72rem; margin: 0 auto; }
.page__header, .actions { display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
h1, p { margin: 0; }
.eyebrow { color: #2563eb; font-weight: 700; text-transform: uppercase; font-size: 0.75rem; }
.page__header p:last-child { margin-top: 0.375rem; color: #64748b; }
.cluster-fields { display: grid; grid-template-columns: 2fr 1fr; gap: 1rem; padding: 1rem; border: 1px solid #cbd5e1; border-radius: 0.75rem; }
label { display: grid; gap: 0.375rem; font-weight: 600; }
.checkbox { display: flex; align-items: center; }
input { padding: 0.625rem; border: 1px solid #cbd5e1; border-radius: 0.375rem; font: inherit; }
.save-error { display: grid; gap: 0.5rem; padding: 1rem; border: 1px solid #f87171; border-radius: 0.5rem; background: #fef2f2; color: #991b1b; }
.field-error { color: #dc2626; }
.actions a, button { padding: 0.5rem 0.75rem; border: 1px solid #94a3b8; border-radius: 0.375rem; background: #fff; }
.primary { background: #2563eb; color: #fff; border-color: #2563eb; }
</style>
