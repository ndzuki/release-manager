import { computed, ref, toRaw } from 'vue';
import { defineStore } from 'pinia';
import {
  createCustomer,
  disableCustomer as disableCustomerRpc,
  getCustomer,
  listCustomerEvents,
  listCustomers,
  mapSaveError,
  updateCustomer,
} from '@/connect/customer-api';
import type { CustomerEvent, CustomerFormInput, CustomerSummary, SaveError } from '@/types/customer';

export const useCustomerStore = defineStore('customers', () => {
  const customers = ref<CustomerSummary[]>([]);
  const loading = ref(false);
  const error = ref<string | null>(null);
  const forbidden = ref(false);
  const notFound = ref(false);

  const current = ref<CustomerSummary | null>(null);
  const history = ref<CustomerEvent[]>([]);
  const historyLoading = ref(false);
  const historyError = ref<string | null>(null);

  const draft = ref<CustomerFormInput | null>(null);
  const serverVersion = ref<number | null>(null);
  const saving = ref(false);
  const saveError = ref<SaveError | null>(null);

  const disabling = ref(false);
  const disableError = ref<string | null>(null);

  const hasCustomers = computed(() => customers.value.length > 0);

  async function loadList(includeDisabled = false) {
    loading.value = true;
    error.value = null;
    forbidden.value = false;
    try {
      customers.value = await listCustomers(includeDisabled);
    } catch (cause) {
      applyLoadError(cause);
    } finally {
      loading.value = false;
    }
  }

  async function loadHistory(customerId: string) {
    historyLoading.value = true;
    historyError.value = null;
    try {
      history.value = await listCustomerEvents(customerId);
    } catch (cause) {
      const mapped = mapSaveError(cause);
      historyError.value = mapped.message;
    } finally {
      historyLoading.value = false;
    }
  }

  async function loadCustomer(customerId: string) {
    loading.value = true;
    error.value = null;
    forbidden.value = false;
    notFound.value = false;
    try {
      const customer = await getCustomer(customerId);
      current.value = customer;
      serverVersion.value = customer.version;
      draft.value = { name: customer.name, slug: customer.slug, version: customer.version };
    } catch (cause) {
      applyLoadError(cause);
    } finally {
      loading.value = false;
    }
  }

  function startCreate() {
    current.value = null;
    serverVersion.value = null;
    saveError.value = null;
    disableError.value = null;
    draft.value = { name: '', slug: '', version: 0 };
  }

  async function save(customerId?: string): Promise<CustomerSummary | null> {
    if (!draft.value) return null;
    saveError.value = null;
    const input = { ...draft.value };

    saving.value = true;
    try {
      const saved = customerId
        ? await updateCustomer(customerId, input)
        : await createCustomer(input);
      current.value = saved;
      serverVersion.value = saved.version;
      draft.value = { name: saved.name, slug: saved.slug, version: saved.version };
      const index = customers.value.findIndex((customer) => customer.id === saved.id);
      if (index >= 0) customers.value[index] = saved;
      else customers.value.unshift(saved);
      if (customerId) await loadHistory(customerId);
      return saved;
    } catch (cause) {
      saveError.value = mapSaveError(cause);
      return null;
    } finally {
      saving.value = false;
    }
  }

  async function disable(customerId: string) {
    // Single-submit guard (AC-051-03): a pending disable ignores repeat calls.
    if (disabling.value) return;
    disabling.value = true;
    disableError.value = null;
    try {
      await disableCustomerRpc(customerId);
      const customer = customers.value.find((item) => item.id === customerId);
      if (customer) customer.status = 'disabled';
      if (current.value?.id === customerId) {
        current.value = { ...current.value, status: 'disabled' };
        draft.value = { ...draft.value!, version: current.value.version };
      }
      await loadHistory(customerId);
    } catch (cause) {
      disableError.value = mapSaveError(cause).message;
    } finally {
      disabling.value = false;
    }
  }

  // Refresh keeps the in-progress draft and rebases its version onto the
  // server state (AC-051-02 refresh-and-retry path).
  async function refreshCustomer(customerId: string) {
    const previousDraft = draft.value ? structuredClone(toRaw(draft.value)) : null;
    await loadCustomer(customerId);
    if (previousDraft && !error.value && !forbidden.value && !notFound.value) {
      draft.value = {
        ...previousDraft,
        version: serverVersion.value ?? previousDraft.version,
      };
      saveError.value = null;
    }
    await loadHistory(customerId);
  }

  function clearSaveError() {
    saveError.value = null;
  }

  function clearDisableError() {
    disableError.value = null;
  }

  function applyLoadError(cause: unknown) {
    const mapped = mapSaveError(cause);
    forbidden.value = mapped.code === 'permission_denied';
    notFound.value = mapped.code === 'not_found';
    error.value = mapped.message;
  }

  return {
    customers,
    loading,
    error,
    forbidden,
    notFound,
    current,
    history,
    historyLoading,
    historyError,
    draft,
    serverVersion,
    saving,
    saveError,
    disabling,
    disableError,
    hasCustomers,
    loadList,
    loadCustomer,
    loadHistory,
    startCreate,
    save,
    disable,
    refreshCustomer,
    clearSaveError,
    clearDisableError,
  };
});
