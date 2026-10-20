<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
import { onMounted } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import { useAuthStore } from '@/stores/auth';
import { useCustomerStore } from '@/stores/customers';

const auth = useAuthStore();
const clusterRoutingEnabled = import.meta.env.VITE_FEATURE_CLUSTER_ROUTING !== 'false';
const store = useCustomerStore();

onMounted(() => store.loadList(true));

/*
 * Direction C (TASK-394). This page is the top of the customer subtree: the audit found
 * neither a breadcrumb nor a category label here, so it gets neither — PageHeader is not
 * asked to invent one. What moves is the heading itself: the h1 becomes the shared
 * `--font-size-h1` (22px) instead of a page-owned title, and the create action rides in
 * PageHeader's action slot instead of a hand-rolled flex header.
 */
</script>

<template>
  <section class="customer-page">
    <PageHeader :title="t('customer.list.title')" :subtitle="t('customer.list.subtitle')">
      <template #actions>
        <RouterLink v-if="auth.canWrite" class="primary" :to="{ name: 'CustomerNew' }">{{ t('customer.list.create') }}</RouterLink>
      </template>
    </PageHeader>

    <LoadingState v-if="store.loading" :message="t('state.loading.customers')" />
    <ForbiddenState v-else-if="store.forbidden" />
    <ErrorState v-else-if="store.error" :message="store.error">
      <button type="button" @click="store.loadList(true)">{{ t('action.retry') }}</button>
    </ErrorState>
    <EmptyState v-else-if="!store.hasCustomers" :title="t('customer.list.empty')" :message="t('customer.list.emptyMessage')">
      <template #action>
        <RouterLink v-if="auth.canWrite" class="primary" :to="{ name: 'CustomerNew' }">{{ t('customer.list.createFirst') }}</RouterLink>
      </template>
    </EmptyState>
    <div v-else class="customer-grid">
      <article v-for="customer in store.customers" :key="customer.id" class="customer-card">
        <header class="customer-card__header">
          <div>
            <h2>{{ customer.name }}</h2>
            <p>{{ customer.slug }}</p>
          </div>
          <!-- Three-way on purpose: `unknown` is a value this console could not read, so
               it must not borrow the copy or the colour of either real state. -->
          <span :class="['status', `status--${customer.status}`]">
            {{ statusLabel('customer', customer.status) }}
          </span>
        </header>
        <dl class="customer-card__facts">
          <div><dt>{{ t('customer.list.version') }}</dt><dd>{{ customer.version }}</dd></div>
          <div v-if="customer.createdAt"><dt>{{ t('customer.list.created') }}</dt><dd>{{ new Date(customer.createdAt).toLocaleDateString() }}</dd></div>
        </dl>
        <div class="customer-card__actions">
          <RouterLink :to="{ name: 'CustomerDetail', params: { id: customer.id } }">{{ t('customer.list.view') }}</RouterLink>
          <!-- Plan N2: one click from the list to the customer's clusters. -->
          <RouterLink
            v-if="clusterRoutingEnabled"
            :to="{ name: 'ClusterList', params: { customerId: customer.id } }"
            :data-testid="`customer-clusters-${customer.id}`"
          >{{ t('nav.clusters') }}</RouterLink>
          <RouterLink v-if="auth.canWrite && customer.status === 'active'" :to="{ name: 'CustomerDetail', params: { id: customer.id } }">{{ t('customer.list.edit') }}</RouterLink>
        </div>
      </article>
    </div>
  </section>
</template>

<style scoped>
.customer-page { display: grid; gap: 1.5rem; max-width: 72rem; margin: 0 auto; }
/* PageHeader keeps a 20px bottom margin for pages that do not stack their sections in a
   grid; this page does, so the gap is the only space between the header and the grid. */
.customer-page :deep(.page-header) { margin-bottom: 0; }
.customer-card__header, .customer-card__actions { display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
.customer-card h2, .customer-card p { margin: 0; }
.customer-card p { color: var(--color-muted); margin-top: 0.375rem; }
.primary { padding: 0.625rem 0.875rem; border-radius: 0.375rem; background: var(--color-primary); color: var(--color-on-accent); text-decoration: none; }
.customer-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(18rem, 1fr)); gap: 1rem; }
/* Direction C card: a surface fill plus the STRONG border. `--color-border` is a hairline
   (~1.3:1 in the dark theme), so a card outlined with it reads as having no outline. */
.customer-card {
  display: grid;
  gap: 1rem;
  padding: 1rem;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
}
.status { font-size: var(--font-size-xs); font-weight: 700; letter-spacing: 0.02em; }
.status--active { color: var(--color-success-ink); }
.status--disabled { color: var(--color-error-strong); }
/* Unreadable is neither success nor failure. */
.status--unknown { color: var(--color-muted-strong); }
.customer-card__facts { display: flex; gap: 1.5rem; margin: 0; }
.customer-card__facts dt { color: var(--color-muted); font-size: var(--font-size-xs); }
.customer-card__facts dd { margin: 0.25rem 0 0; font-weight: 700; }
.customer-card__actions { justify-content: flex-start; }
</style>
