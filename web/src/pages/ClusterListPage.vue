<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onMounted, ref } from 'vue';
import { useRoute } from 'vue-router';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import ClusterTargetSelect from '@/components/clusters/ClusterTargetSelect.vue';
import { useScopeNames } from '@/composables/useScopeNames';
import { useClusterStore } from '@/stores/cluster';
import { useAuthStore } from '@/stores/auth';

const route = useRoute();
const store = useClusterStore();
const auth = useAuthStore();
const selectedTarget = ref('');
const customerId = String(route.params.customerId);

/*
 * Direction C (TASK-394). The page had no breadcrumb and no category label, so it gets
 * no eyebrow — but it does sit under a customer, and the trail names that customer.
 * `useScopeNames` resolves the name from the query or the API; its last resort is the
 * route identifier, and a crumb that would print a UUID is dropped instead.
 */
const { customerName } = useScopeNames({ resolveCluster: false });
const customerLabel = computed(() =>
  customerName.value && customerName.value !== customerId ? customerName.value : '',
);
const crumbs = computed(() => {
  const trail: Array<{ label: string }> = [];
  if (customerLabel.value) trail.push({ label: customerLabel.value });
  return trail;
});

onMounted(() => store.loadList(customerId));
</script>

<template>
  <section class="page">
    <PageHeader
      :title="t('cluster.list.title')"
      :subtitle="t('cluster.list.subtitle')"
      :crumbs="crumbs"
    >
      <template #actions>
        <RouterLink v-if="auth.canWrite" :to="{ name: 'ClusterNew', params: { customerId } }" class="primary">{{ t('cluster.list.create') }}</RouterLink>
      </template>
    </PageHeader>

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
/* PageHeader keeps a 20px bottom margin for pages that do not stack their sections in a
   grid; this page does, so the gap is the only space between the header and the content. */
.page :deep(.page-header) { margin-bottom: 0; }
.cluster-card header, .actions { display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
h2, p { margin: 0; }
.cluster-card p { color: var(--color-muted); margin-top: 0.375rem; }
.primary { padding: 0.625rem 0.875rem; border-radius: 0.375rem; background: var(--color-primary); color: var(--color-on-accent); text-decoration: none; }
.cluster-content { display: grid; gap: 1.5rem; }
.cluster-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(18rem, 1fr)); gap: 1rem; }
/* Direction C card: surface fill + the STRONG border (`--color-border` is a hairline and
   vanishes in the dark theme). */
.cluster-card, .target-select {
  display: grid;
  gap: 1rem;
  padding: 1rem;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
}
.status { display: inline-block; margin-top: 0.375rem; font-size: var(--font-size-xs); font-weight: 700; }
.status--active { color: var(--color-success-ink); }
.status--disabled { color: var(--color-error-strong); }
.actions { justify-content: flex-start; }
</style>
