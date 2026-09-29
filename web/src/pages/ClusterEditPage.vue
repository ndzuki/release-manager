<script setup lang="ts">
import { t } from '@/i18n/messages';
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
    <LoadingState v-if="store.loading" :message="t('state.loading.cluster')" />
    <ForbiddenState v-else-if="store.forbidden" />
    <ErrorState v-else-if="store.notFound" :message="t('cluster.notFound')">
      <RouterLink :to="{ name: 'ClusterList', params: { customerId } }">{{ t('cluster.detail.back') }}</RouterLink>
    </ErrorState>

    <form v-else-if="store.draft" class="cluster-form" @submit.prevent="handleSave">
      <header class="page__header">
        <div>
          <p class="eyebrow">{{ isCreate ? t('cluster.edit.newLabel') : t('cluster.edit.editLabel') }}</p>
          <h1>{{ isCreate ? t('cluster.edit.createTitle') : store.current?.name }}</h1>
          <p>{{ t('cluster.edit.credentials') }}</p>
        </div>
        <div class="actions">
          <RouterLink :to="clusterId ? { name: 'ClusterDetail', params: { customerId, clusterId } } : { name: 'ClusterList', params: { customerId } }">{{ t('action.cancel') }}</RouterLink>
          <button type="submit" class="primary" :disabled="store.saving">
            {{ store.saving ? t('cluster.edit.saving') : t('cluster.edit.save') }}
          </button>
        </div>
      </header>

      <div v-if="store.saveError" class="save-error" role="alert">
        <strong>{{ store.saveError.code === 'optimistic_lock_conflict' ? t('cluster.edit.conflict') : store.saveError.message }}</strong>
        <span v-if="store.saveError.code === 'optimistic_lock_conflict'">{{ t('cluster.edit.draftPreserved') }}</span>
        <button v-if="store.saveError.code === 'network_error'" type="submit">{{ t('cluster.edit.retrySave') }}</button>
        <button v-if="store.saveError.code === 'optimistic_lock_conflict' && clusterId" type="button" @click="store.refreshCluster(clusterId)">{{ t('cluster.edit.refreshVersion') }}</button>
      </div>

      <section class="cluster-fields">
        <label>
          {{ t('cluster.edit.name') }}
          <input v-model="store.draft.name" maxlength="254" :aria-invalid="Boolean(store.saveError?.fieldViolations?.some((item) => item.field === 'name'))" />
          <small v-for="error in store.saveError?.fieldViolations?.filter((item) => item.field === 'name')" :key="error.description" class="field-error">{{ error.description }}</small>
        </label>
        <label class="checkbox">
          <input v-model="store.draft.enabled" type="checkbox" />
          {{ t('cluster.edit.enabled') }}
        </label>
      </section>

      <RouteRuleEditor
        :title="t('cluster.edit.imageRoutes')"
        artifact-type="image"
        :rules="store.draft.imageRules"
        :violations="store.saveError?.fieldViolations"
        :conflicting-rule-id="store.saveError?.conflictingRuleId"
        :endpoints="endpoints"
        @add="addRule('image')"
        @remove="removeRule('image', $event)"
      />
      <RouteRuleEditor
        :title="t('cluster.edit.chartRoutes')"
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
.eyebrow { color: var(--color-primary); font-weight: 700; text-transform: uppercase; font-size: var(--font-size-xs); }
.page__header p:last-child { margin-top: 0.375rem; color: var(--color-muted); }
.cluster-fields { display: grid; grid-template-columns: 2fr 1fr; gap: 1rem; padding: 1rem; border: 1px solid var(--color-border-strong); border-radius: 0.75rem; }
label { display: grid; gap: 0.375rem; font-weight: 600; }
.checkbox { display: flex; align-items: center; }
input { padding: 0.625rem; border: 1px solid var(--color-border-strong); border-radius: 0.375rem; font: inherit; }
.save-error { display: grid; gap: 0.5rem; padding: 1rem; border: 1px solid var(--color-danger-border-bright); border-radius: 0.5rem; background: var(--color-danger-soft); color: var(--color-error-strong); }
.field-error { color: var(--color-danger); }
.actions a, button { padding: 0.5rem 0.75rem; border: 1px solid var(--color-subtle); border-radius: 0.375rem; background: var(--color-surface); }
.primary { background: var(--color-primary); color: var(--color-on-accent); border-color: var(--color-primary); }
</style>
