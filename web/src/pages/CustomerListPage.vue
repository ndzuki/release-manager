<script setup lang="ts">
import { t } from '@/i18n/messages';
import { onMounted } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import { useAuthStore } from '@/stores/auth';
import { useCustomerStore } from '@/stores/customers';

const auth = useAuthStore();
const clusterRoutingEnabled = import.meta.env.VITE_FEATURE_CLUSTER_ROUTING !== 'false';
const store = useCustomerStore();

onMounted(() => store.loadList(true));
</script>

<template>
  <section class="customer-page">
    <header class="customer-page__header">
      <div>
        <h1>{{ t('customer.list.title') }}</h1>
        <p>{{ t('customer.list.subtitle') }}</p>
      </div>
      <RouterLink v-if="auth.canWrite" class="primary" :to="{ name: 'CustomerNew' }">{{ t('customer.list.create') }}</RouterLink>
    </header>

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
          <span :class="['status', customer.status === 'active' ? 'status--active' : 'status--disabled']">
            {{ customer.status === 'active' ? t('status.active') : t('status.disabled') }}
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
.customer-page__header, .customer-card__header, .customer-card__actions { display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
.customer-page__header h1, .customer-page__header p, .customer-card h2, .customer-card p { margin: 0; }
.customer-page__header p, .customer-card p { color: var(--color-muted); margin-top: 0.375rem; }
.primary { padding: 0.625rem 0.875rem; border-radius: 0.375rem; background: var(--color-primary); color: var(--color-on-accent); text-decoration: none; }
.customer-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(18rem, 1fr)); gap: 1rem; }
.customer-card { display: grid; gap: 1rem; padding: 1rem; border: 1px solid var(--color-border-strong); border-radius: 0.75rem; }
.status { font-size: var(--font-size-xs); font-weight: 700; text-transform: uppercase; }
.status--active { color: var(--color-success); }
.status--disabled { color: var(--color-error); }
.customer-card__facts { display: flex; gap: 1.5rem; margin: 0; }
.customer-card__facts dt { color: var(--color-muted); font-size: var(--font-size-xs); }
.customer-card__facts dd { margin: 0.25rem 0 0; font-weight: 700; }
.customer-card__actions { justify-content: flex-start; }
</style>
