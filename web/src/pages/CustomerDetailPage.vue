<script setup lang="ts">
import { computed, onMounted, shallowRef, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import CustomerForm from '@/components/customers/CustomerForm.vue';
import CustomerHistory from '@/components/customers/CustomerHistory.vue';
import DisableCustomerDialog from '@/components/customers/DisableCustomerDialog.vue';
import { useAuthStore } from '@/stores/auth';
import { useCustomerStore } from '@/stores/customers';
import type { CustomerFormInput } from '@/types/customer';

const route = useRoute();
const router = useRouter();
const auth = useAuthStore();
const store = useCustomerStore();
const showDisableDialog = shallowRef(false);

const customerId = computed(() => typeof route.params.id === 'string' ? route.params.id : '');
const isCreate = computed(() => customerId.value === '');
const canWrite = computed(() => auth.canWrite && !isCreate.value);
const title = computed(() => isCreate.value ? 'Create customer' : (store.current?.name ?? 'Customer'));
const form = computed<CustomerFormInput | null>(() => store.draft);

onMounted(async () => {
  if (isCreate.value) store.startCreate();
  else {
    await store.loadCustomer(customerId.value);
    await store.loadHistory(customerId.value);
  }
});

// CustomerNew and CustomerDetail share this component; Vue Router reuses the
// instance when the create form redirects to the new customer's detail route,
// so onMounted does not re-run. Reload the detail + history on route changes.
watch(customerId, async (id, previous) => {
  if (id === previous) return;
  if (isCreate.value) store.startCreate();
  else {
    await store.loadCustomer(id);
    await store.loadHistory(id);
  }
});

async function save() {
  const saved = await store.save(isCreate.value ? undefined : customerId.value);
  if (saved && isCreate.value) await router.replace({ name: 'CustomerDetail', params: { id: saved.id } });
}

async function refresh() {
  if (!isCreate.value) await store.refreshCustomer(customerId.value);
}

async function disable() {
  if (isCreate.value || !store.current) return;
  await store.disable(store.current.id);
  showDisableDialog.value = false;
}
</script>

<template>
  <section class="customer-detail">
    <LoadingState v-if="store.loading && !isCreate" message="Loading customer…" />
    <ForbiddenState v-else-if="store.forbidden" />
    <ErrorState v-else-if="store.error" :message="store.error">
      <button type="button" @click="router.push({ name: 'CustomerList' })">Back to customers</button>
    </ErrorState>
    <template v-else>
      <header class="customer-detail__header">
        <div>
          <p class="eyebrow">Customer</p>
          <h1>{{ title }}</h1>
          <p v-if="store.current" :class="store.current.status === 'disabled' ? 'disabled-copy' : 'muted'">
            {{ store.current.status === 'disabled' ? 'Disabled — read-only history remains available.' : 'Tenant boundary for release management.' }}
          </p>
        </div>
        <div class="customer-detail__actions">
          <RouterLink :to="{ name: 'CustomerList' }">Back</RouterLink>
          <button v-if="canWrite && store.current?.status === 'active'" type="button" class="danger" @click="showDisableDialog = true">Disable customer</button>
        </div>
      </header>

      <div v-if="store.current?.status === 'disabled'" class="disabled-banner" role="status">
        This customer is disabled. Editing and disabling are unavailable.
      </div>
      <div v-if="isCreate && !auth.canWrite" class="readonly-banner" role="status">
        Your viewer role has no customer creation entry.
      </div>

      <section v-if="form && (!isCreate || auth.canWrite)" class="customer-detail__panel">
        <h2>{{ isCreate ? 'Customer details' : 'Edit customer' }}</h2>
        <div v-if="store.saveError?.code === 'optimistic_lock_conflict'" class="conflict-banner" role="alert">
          This customer changed since you opened it. Refresh to rebase your draft, then retry saving.
          <button type="button" @click="refresh">Refresh</button>
        </div>
        <ErrorState v-else-if="store.saveError" :message="store.saveError.message">
          <button type="button" @click="store.clearSaveError()">Dismiss</button>
        </ErrorState>
        <CustomerForm
          v-model="store.draft!"
          :readonly="!auth.canWrite || store.current?.status === 'disabled'"
          :submitting="store.saving"
          :submit-label="isCreate ? 'Create customer' : 'Save changes'"
          :field-violations="store.saveError?.fieldViolations"
          @submit="save"
        />
      </section>

      <CustomerHistory
        v-if="!isCreate && store.current"
        :events="store.history"
        :loading="store.historyLoading"
        :error="store.historyError"
        @retry="store.loadHistory(customerId)"
      />

      <ErrorState v-if="store.disableError" :message="store.disableError">
        <button type="button" @click="store.clearDisableError()">Dismiss</button>
      </ErrorState>
    </template>

    <DisableCustomerDialog
      :open="showDisableDialog"
      :pending="store.disabling"
      @cancel="showDisableDialog = false"
      @confirm="disable"
    />
  </section>
</template>

<style scoped>
.customer-detail { display: grid; gap: 1.5rem; max-width: 72rem; margin: 0 auto; }
.customer-detail__header, .customer-detail__actions { display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
.customer-detail__header h1, .customer-detail__header p { margin: 0; }
.eyebrow { color: #2563eb; font-size: 0.75rem; font-weight: 700; text-transform: uppercase; }
.muted { color: #64748b; margin-top: 0.375rem !important; }
.disabled-copy { color: #b91c1c; margin-top: 0.375rem !important; }
.customer-detail__actions a, .customer-detail__actions button { padding: 0.5rem 0.75rem; border: 1px solid #94a3b8; border-radius: 0.375rem; background: #fff; text-decoration: none; }
.danger { color: #b91c1c; cursor: pointer; }
.customer-detail__panel { display: grid; gap: 1rem; padding: 1rem; border: 1px solid #cbd5e1; border-radius: 0.75rem; }
.customer-detail__panel h2 { margin: 0; font-size: 1.1rem; }
.disabled-banner, .readonly-banner, .conflict-banner { display: flex; align-items: center; justify-content: space-between; gap: 1rem; padding: 0.875rem 1rem; border-radius: 0.5rem; }
.disabled-banner { background: #fef2f2; color: #991b1b; }
.readonly-banner { background: #f1f5f9; color: #475569; }
.conflict-banner { background: #fff7ed; color: #9a3412; }
.conflict-banner button { padding: 0.4rem 0.65rem; border: 1px solid #c2410c; border-radius: 0.375rem; background: #fff; color: #9a3412; cursor: pointer; }
</style>
