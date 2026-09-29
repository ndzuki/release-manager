<script setup lang="ts">
import { t } from '@/i18n/messages';
import { onMounted, ref } from 'vue';
import { useRoute } from 'vue-router';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import ClusterTargetSelect from '@/components/clusters/ClusterTargetSelect.vue';
import { useClusterStore } from '@/stores/cluster';
import { useAuthStore } from '@/stores/auth';

const route = useRoute();
const store = useClusterStore();
const auth = useAuthStore();
const selectedTarget = ref('');
const customerId = String(route.params.customerId);

onMounted(() => store.loadList(customerId));
</script>

<template>
  <section class="page">
    <header class="page__header">
      <div>
        <h1>{{ t('cluster.list.title') }}</h1>
        <p>{{ t('cluster.list.subtitle') }}</p>
      </div>
      <RouterLink v-if="auth.canWrite" :to="{ name: 'ClusterNew', params: { customerId } }" class="primary">{{ t('cluster.list.create') }}</RouterLink>
    </header>

    <LoadingState v-if="store.loading" :message="t('state.loading.clusters')" />
    <ForbiddenState v-else-if="store.forbidden" />
    <ErrorState v-else-if="store.error" :message="store.error">
      <button type="button" @click="store.loadList(customerId)">{{ t('action.retry') }}</button>
    </ErrorState>
    <EmptyState v-else-if="!store.hasClusters" :message="t('cluster.list.empty')">
      <template #action>
        <RouterLink v-if="auth.canWrite" :to="{ name: 'ClusterNew', params: { customerId } }" class="primary">{{ t('cluster.list.createFirst') }}</RouterLink>
      </template>
    </EmptyState>

    <div v-else class="cluster-content">
      <div class="cluster-grid">
        <article v-for="cluster in store.clusters" :key="cluster.id" class="cluster-card">
          <header>
            <div>
              <h2>{{ cluster.name }}</h2>
              <span :class="cluster.enabled ? 'status status--active' : 'status status--disabled'">
                {{ cluster.enabled ? t('status.active') : t('status.disabled') }}
              </span>
            </div>
            <span>v{{ cluster.version }}</span>
          </header>
          <p>{{ cluster.routeCount }} {{ t('cluster.list.routingRules') }}</p>
          <div class="actions">
            <RouterLink :to="{ name: 'ClusterDetail', params: { customerId, clusterId: cluster.id } }">{{ t('customer.list.view') }}</RouterLink>
            <RouterLink v-if="auth.canWrite" :to="{ name: 'ClusterEdit', params: { customerId, clusterId: cluster.id } }">{{ t('customer.list.edit') }}</RouterLink>
          </div>
        </article>
      </div>

      <aside class="target-select">
        <h2>{{ t('cluster.list.preview') }}</h2>
        <ClusterTargetSelect v-model="selectedTarget" :clusters="store.clusters" />
      </aside>
    </div>
  </section>
</template>

<style scoped>
.page { display: grid; gap: 1.5rem; max-width: 72rem; margin: 0 auto; }
.page__header, .cluster-card header, .actions { display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
h1, h2, p { margin: 0; }
.page__header p, .cluster-card p { color: var(--color-muted); margin-top: 0.375rem; }
.primary { padding: 0.625rem 0.875rem; border-radius: 0.375rem; background: var(--color-primary); color: white; text-decoration: none; }
.cluster-content { display: grid; gap: 1.5rem; }
.cluster-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(18rem, 1fr)); gap: 1rem; }
.cluster-card, .target-select { display: grid; gap: 1rem; padding: 1rem; border: 1px solid var(--color-border-strong); border-radius: 0.75rem; }
.status { display: inline-block; margin-top: 0.375rem; font-size: var(--font-size-xs); font-weight: 700; }
.status--active { color: var(--color-success); }
.status--disabled { color: var(--color-error); }
.actions { justify-content: flex-start; }
</style>
