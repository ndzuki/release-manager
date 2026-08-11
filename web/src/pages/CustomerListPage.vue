<script setup lang="ts">
import { onMounted } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import { useAuthStore } from '@/stores/auth';
import { useCustomerStore } from '@/stores/customers';

const auth = useAuthStore();
const store = useCustomerStore();

onMounted(() => store.loadList(true));
</script>

<template>
  <section class="customer-page">
    <header class="customer-page__header">
      <div>
        <h1>Customers</h1>
        <p>Manage customer tenants available to the active organization.</p>
      </div>
      <RouterLink v-if="auth.canWrite" class="primary" :to="{ name: 'CustomerNew' }">Create customer</RouterLink>
    </header>

    <LoadingState v-if="store.loading" message="Loading customers…" />
    <ForbiddenState v-else-if="store.forbidden" />
    <ErrorState v-else-if="store.error" :message="store.error">
      <button type="button" @click="store.loadList(true)">Retry</button>
    </ErrorState>
    <EmptyState v-else-if="!store.hasCustomers" title="No customers" message="No customers are bound to the active organization.">
      <template #action>
        <RouterLink v-if="auth.canWrite" class="primary" :to="{ name: 'CustomerNew' }">Create the first customer</RouterLink>
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
            {{ customer.status === 'active' ? 'Active' : 'Disabled' }}
          </span>
        </header>
        <dl class="customer-card__facts">
          <div><dt>Version</dt><dd>{{ customer.version }}</dd></div>
          <div v-if="customer.createdAt"><dt>Created</dt><dd>{{ new Date(customer.createdAt).toLocaleDateString() }}</dd></div>
        </dl>
        <div class="customer-card__actions">
          <RouterLink :to="{ name: 'CustomerDetail', params: { id: customer.id } }">View</RouterLink>
          <RouterLink v-if="auth.canWrite && customer.status === 'active'" :to="{ name: 'CustomerDetail', params: { id: customer.id } }">Edit</RouterLink>
        </div>
      </article>
    </div>
  </section>
</template>

<style scoped>
.customer-page { display: grid; gap: 1.5rem; max-width: 72rem; margin: 0 auto; }
.customer-page__header, .customer-card__header, .customer-card__actions { display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
.customer-page__header h1, .customer-page__header p, .customer-card h2, .customer-card p { margin: 0; }
.customer-page__header p, .customer-card p { color: #64748b; margin-top: 0.375rem; }
.primary { padding: 0.625rem 0.875rem; border-radius: 0.375rem; background: #2563eb; color: #fff; text-decoration: none; }
.customer-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(18rem, 1fr)); gap: 1rem; }
.customer-card { display: grid; gap: 1rem; padding: 1rem; border: 1px solid #cbd5e1; border-radius: 0.75rem; }
.status { font-size: 0.75rem; font-weight: 700; text-transform: uppercase; }
.status--active { color: #15803d; }
.status--disabled { color: #b91c1c; }
.customer-card__facts { display: flex; gap: 1.5rem; margin: 0; }
.customer-card__facts dt { color: #64748b; font-size: 0.75rem; }
.customer-card__facts dd { margin: 0.25rem 0 0; font-weight: 700; }
.customer-card__actions { justify-content: flex-start; }
</style>
