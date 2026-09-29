<script setup lang="ts">
import { t } from '@/i18n/messages';
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
const clusterRoutingEnabled = import.meta.env.VITE_FEATURE_CLUSTER_ROUTING !== 'false';
const store = useCustomerStore();
const showDisableDialog = shallowRef(false);

const customerId = computed(() => typeof route.params.id === 'string' ? route.params.id : '');
const isCreate = computed(() => customerId.value === '');
const canWrite = computed(() => auth.canWrite && !isCreate.value);
const title = computed(() => isCreate.value ? t('customer.detail.create') : (store.current?.name ?? t('customer.detail.label')));
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
    <LoadingState v-if="store.loading && !isCreate" :message="t('state.loading.customer')" />
    <ForbiddenState v-else-if="store.forbidden" />
    <ErrorState v-else-if="store.error" :message="store.error">
      <button type="button" @click="router.push({ name: 'CustomerList' })">{{ t('customer.detail.back') }}</button>
    </ErrorState>
    <template v-else>
      <header class="customer-detail__header">
        <div>
          <p class="eyebrow">{{ t('customer.detail.label') }}</p>
          <h1>{{ title }}</h1>
          <p v-if="store.current" :class="store.current.status === 'disabled' ? 'disabled-copy' : 'muted'">
            {{ store.current.status === 'disabled' ? t('customer.detail.disabledBanner') : t('customer.detail.subtitle') }}
          </p>
        </div>
        <div class="customer-detail__actions">
          <!-- Plan N2/N8: the customer subtree had no path to clusters at all, so the
               release subtree was only reachable by typing a URL. -->
          <RouterLink
            v-if="!isCreate && clusterRoutingEnabled && store.current"
            class="primary"
            :to="{ name: 'ClusterList', params: { customerId: store.current.id } }"
          >{{ t('nav.clusters') }}</RouterLink>
          <RouterLink :to="{ name: 'CustomerList' }">{{ t('customer.detail.back') }}</RouterLink>
          <button v-if="canWrite && store.current?.status === 'active'" type="button" class="danger" @click="showDisableDialog = true">{{ t('customer.detail.disable') }}</button>
        </div>
      </header>

      <div v-if="store.current?.status === 'disabled'" class="disabled-banner" role="status">
        {{ t('customer.detail.disabled') }}
      </div>
      <div v-if="isCreate && !auth.canWrite" class="readonly-banner" role="status">
        {{ t('customer.detail.readonly') }}
      </div>

      <section v-if="form && (!isCreate || auth.canWrite)" class="customer-detail__panel">
        <h2>{{ isCreate ? t('customer.detail.details') : t('customer.detail.editTitle') }}</h2>
        <div v-if="store.saveError?.code === 'optimistic_lock_conflict'" class="conflict-banner" role="alert">
          {{ t('customer.detail.stale') }}
          <button type="button" @click="refresh">{{ t('action.refresh') }}</button>
        </div>
        <ErrorState v-else-if="store.saveError" :message="store.saveError.message">
          <button type="button" @click="store.clearSaveError()">{{ t('action.dismiss') }}</button>
        </ErrorState>
        <CustomerForm
          v-model="store.draft!"
          :readonly="!auth.canWrite || store.current?.status === 'disabled'"
          :submitting="store.saving"
          :submit-label="isCreate ? t('customer.detail.create') : t('customer.detail.save')"
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
        <button type="button" @click="store.clearDisableError()">{{ t('action.dismiss') }}</button>
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
.eyebrow { color: var(--color-primary); font-size: var(--font-size-xs); font-weight: 700; text-transform: uppercase; }
.muted { color: var(--color-muted); margin-top: 0.375rem !important; }
.disabled-copy { color: var(--color-error); margin-top: 0.375rem !important; }
.customer-detail__actions a, .customer-detail__actions button { padding: 0.5rem 0.75rem; border: 1px solid var(--color-subtle); border-radius: 0.375rem; background: var(--color-surface); text-decoration: none; }
.danger { color: var(--color-error); cursor: pointer; }
.customer-detail__panel { display: grid; gap: 1rem; padding: 1rem; border: 1px solid var(--color-border-strong); border-radius: 0.75rem; }
.customer-detail__panel h2 { margin: 0; font-size: var(--font-size-lg); }
.disabled-banner, .readonly-banner, .conflict-banner { display: flex; align-items: center; justify-content: space-between; gap: 1rem; padding: 0.875rem 1rem; border-radius: 0.5rem; }
.disabled-banner { background: var(--color-danger-soft); color: var(--color-error-strong); }
.readonly-banner { background: var(--color-surface-muted); color: var(--color-muted-strong); }
.conflict-banner { background: var(--color-warning-soft); color: var(--color-warning-ink); }
.conflict-banner button { padding: 0.4rem 0.65rem; border: 1px solid var(--color-danger-orange); border-radius: 0.375rem; background: var(--color-surface); color: var(--color-warning-ink); cursor: pointer; }
</style>
