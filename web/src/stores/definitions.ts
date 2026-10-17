import { computed, ref } from 'vue';
import { defineStore } from 'pinia';
import { t } from '@/i18n/messages';
import {
  createDefinition,
  disableDefinition,
  listDefinitions,
  mapDefinitionError,
  updateDefinition,
  type CreateDefinitionInput,
  type DefinitionFailure,
  type DefinitionFilters,
  type DefinitionView,
  type UpdateDefinitionInput,
} from '@/connect/definition-api';

/*
 * Release definition management (REQ-040, A9).
 *
 * Update is optimistically locked only when we send the version we read, so the
 * store always sends it and reloads on a conflict: the operator must act on the
 * definition the server currently holds.
 */
export const useDefinitionsStore = defineStore('definitions', () => {
  const filters = ref<DefinitionFilters>({ customerId: '', clusterId: '', includeDisabled: true });
  const definitions = ref<DefinitionView[]>([]);
  const loading = ref(false);
  const saving = ref(false);
  const failure = ref<DefinitionFailure | null>(null);
  const notice = ref('');

  const isEmpty = computed(() => !loading.value && !failure.value && definitions.value.length === 0);

  /** Definitions whose stored promotion JSON could not be decoded (contract violation). */
  const violations = computed(() => definitions.value.filter((definition) => definition.promotionMappingsViolation));

  async function fetchDefinitions(): Promise<void> {
    loading.value = true;
    try {
      definitions.value = await listDefinitions(filters.value);
    } catch (error) {
      failure.value = mapDefinitionError(error);
      definitions.value = [];
    } finally {
      loading.value = false;
    }
  }

  async function load(next?: DefinitionFilters): Promise<void> {
    if (next) filters.value = next;
    failure.value = null;
    notice.value = '';
    await fetchDefinitions();
  }

  async function update(input: UpdateDefinitionInput): Promise<boolean> {
    saving.value = true;
    failure.value = null;
    notice.value = '';
    try {
      const updated = await updateDefinition(input);
      await fetchDefinitions();
      notice.value = t('definitions.update.success', { name: updated.name, version: String(updated.version) });
      return true;
    } catch (error) {
      // Reload first so the operator sees the version the server holds, then report
      // (a plain load() would wipe the message).
      const mapped = mapDefinitionError(error);
      await fetchDefinitions();
      failure.value = mapped;
      return false;
    } finally {
      saving.value = false;
    }
  }

  /**
   * Creates a definition and reloads the list.
   *
   * Unlike `update`, a failed create does not reload: nothing was written, and an
   * ALREADY_EXISTS refusal refers to a row the operator can already see in the list.
   */
  async function create(input: CreateDefinitionInput): Promise<boolean> {
    saving.value = true;
    failure.value = null;
    notice.value = '';
    try {
      const created = await createDefinition(input);
      await fetchDefinitions();
      notice.value = t('definitions.create.success', { name: created.name, version: String(created.version) });
      return true;
    } catch (error) {
      failure.value = mapDefinitionError(error);
      return false;
    } finally {
      saving.value = false;
    }
  }

  /**
   * Disables a definition with the version the page read.
   *
   * Disabling is versioned exactly like update, so a lost race (FAILED_PRECONDITION
   * + optimistic_lock_conflict) reloads first: the page can then adopt the version
   * the server holds and the retry succeeds instead of failing forever on a stale one.
   */
  async function disable(definition: DefinitionView): Promise<boolean> {
    saving.value = true;
    failure.value = null;
    notice.value = '';
    try {
      const disabled = await disableDefinition(definition.id, definition.version);
      await fetchDefinitions();
      notice.value = t('definitions.disable.success', { name: disabled.name || definition.name });
      return true;
    } catch (error) {
      const mapped = mapDefinitionError(error);
      if (mapped.code === 'conflict') await fetchDefinitions();
      failure.value = mapped;
      return false;
    } finally {
      saving.value = false;
    }
  }

  return {
    filters,
    definitions,
    loading,
    saving,
    failure,
    notice,
    isEmpty,
    violations,
    load,
    update,
    create,
    disable,
  };
});
