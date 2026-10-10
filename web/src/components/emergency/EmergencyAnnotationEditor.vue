<script setup lang="ts">
import { t } from '@/i18n/messages';
// Annotation batch editor (plan v3 Step 4, AC-058-02/13): rows are bound to
// the definition's approved annotation keys (whitelist), share one scope, use
// stable local IDs, and validate through the pure rules in
// features/emergency/validation.ts. The whitelist is the definition's
// `approved_annotation_keys`, so a key that is approved but NOT yet observed on
// the workload is selectable too; `observedKeys` labels which keys the read
// model has actually seen (TASK-274). The batch is submitted with the emergency
// change (ExecuteEmergencyChangeRequest.annotations + annotation_scope) when
// the page's action selector is on 'annotations'; the server re-validates every
// (key, scope) pair against the definition's whitelist.
import { computed, ref, useId, watch } from 'vue';
import {
  ANNOTATION_MAX_ENTRIES,
  validateAnnotationEntries,
  type AnnotationEntryDraft,
} from '@/features/emergency/validation';

const props = withDefaults(
  defineProps<{
    /** Server-approved annotation keys for `scope` (whitelist projection). */
    approvedKeys: string[];
    scope: string;
    values: Array<{ localId: string; key: string; value: string; scope: string }>;
    /** Scopes the target approves keys for; when empty the row scope is fixed. */
    availableScopes?: string[];
    /**
     * Keys with a fresh observation on the workload. When supplied, a key
     * outside it is labelled "approved, not yet observed" so the operator can
     * tell the whitelist from the observation (TASK-274); null means the
     * observation state is unknown and the bare key is shown.
     */
    observedKeys?: string[] | null;
    /** Why the full whitelist is unavailable, when it is (TASK-274). */
    whitelistNotice?: string | null;
    /** Server-side rejection routed to this field (D7 double-track). */
    error?: string | null;
  }>(),
  { availableScopes: () => [], observedKeys: null, whitelistNotice: null, error: null },
);

const emit = defineEmits<{
  update: [entries: AnnotationEntryDraft[]];
  'update:scope': [scope: string];
}>();

const nextLocalId = ref(1);

const drafts = computed<AnnotationEntryDraft[]>(() => props.values);

const validation = computed(() =>
  validateAnnotationEntries(
    drafts.value,
    props.approvedKeys.map((key) => ({ key, scope: props.scope })),
  ),
);

const canAdd = computed(() => props.approvedKeys.length > 0 && drafts.value.length < ANNOTATION_MAX_ENTRIES);

/*
 * Ids for the per-row key/value controls. Prefix from useId() so two editors on one
 * page cannot produce the same id, and stable across re-renders. The row cell's text
 * column header (Key/值) does not name the control, so each control owns a
 * visually-hidden <label for> carrying the column name plus the row number.
 */
const rowIdPrefix = useId();
const errorId = computed(() => `${rowIdPrefix}-annotation-error`);

function rowFieldId(localId: string, field: 'key' | 'value'): string {
  return `${rowIdPrefix}-annotation-${localId}-${field}`;
}

/**
 * Option label for a whitelisted key. `observedKeys` (when supplied) comes from
 * the read model, which projects approved ∩ OBSERVED; every other whitelist
 * entry is approved but has not appeared on the workload yet, and the operator
 * should see which is which before submitting (TASK-274).
 */
function keyOptionLabel(key: string): string {
  if (!props.observedKeys) return key;
  return props.observedKeys.includes(key) ? t('annotation.key.observed', { key }) : t('annotation.key.notObserved', { key });
}

function addRow(): void {
  if (!canAdd.value) return;
  const used = new Set(drafts.value.map((entry) => entry.key));
  const key = props.approvedKeys.find((candidate) => !used.has(candidate)) ?? props.approvedKeys[0];
  emit('update', [...drafts.value, { localId: `local-${nextLocalId.value++}`, key, value: '', scope: props.scope }]);
}

function removeRow(localId: string): void {
  emit('update', drafts.value.filter((entry) => entry.localId !== localId));
}

function updateRow(localId: string, patch: Partial<AnnotationEntryDraft>): void {
  emit(
    'update',
    drafts.value.map((entry) => (entry.localId === localId ? { ...entry, ...patch } : entry)),
  );
}

watch(
  () => props.scope,
  () => {
    // Scope change re-anchors every row to the new scope (AC-058-25).
    emit(
      'update',
      drafts.value.map((entry) => ({ ...entry, scope: props.scope })),
    );
  },
);
</script>

<template>
  <div class="annotation-editor">
    <p class="hint">{{ t('annotation.hint') }}</p>
    <p v-if="whitelistNotice" class="hint" role="status">{{ whitelistNotice }}</p>
    <label v-if="availableScopes.length > 0" class="scope-field">
      <span class="scope-label">{{ t('annotation.scope') }}</span>
      <select
        class="scope-select"
        :value="scope"
        @change="emit('update:scope', ($event.target as HTMLSelectElement).value)"
      >
        <option v-for="candidate in availableScopes" :key="candidate" :value="candidate">{{ candidate }}</option>
      </select>
    </label>
    <table class="annotation-table">
      <thead>
        <tr>
          <th scope="col">{{ t('annotation.keyWhitelist') }}</th>
          <th scope="col">{{ t('annotation.value') }}</th>
          <th scope="col">{{ t('annotation.scope') }}</th>
          <th scope="col"></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(entry, index) in drafts" :key="entry.localId">
          <td>
            <label class="visually-hidden" :for="rowFieldId(entry.localId, 'key')">
              {{ t('common.labelledRowField', { field: t('annotation.keyWhitelist'), index: index + 1 }) }}
            </label>
            <select
              :id="rowFieldId(entry.localId, 'key')"
              class="field-input"
              :value="entry.key"
              :aria-invalid="validation.valid ? undefined : true"
              :aria-describedby="validation.valid ? undefined : errorId"
              @change="updateRow(entry.localId, { key: ($event.target as HTMLSelectElement).value })"
            >
              <option v-for="key in approvedKeys" :key="key" :value="key">{{ keyOptionLabel(key) }}</option>
            </select>
          </td>
          <td>
            <label class="visually-hidden" :for="rowFieldId(entry.localId, 'value')">
              {{ t('common.labelledRowField', { field: t('annotation.value'), index: index + 1 }) }}
            </label>
            <input
              :id="rowFieldId(entry.localId, 'value')"
              class="field-input"
              :value="entry.value"
              :placeholder="t('annotation.valuePlaceholder')"
              :aria-invalid="validation.valid ? undefined : true"
              :aria-describedby="validation.valid ? undefined : errorId"
              @input="updateRow(entry.localId, { value: ($event.target as HTMLInputElement).value })"
            />
          </td>
          <td>{{ entry.scope }}</td>
          <td>
            <button
              type="button"
              class="row-remove"
              :aria-label="t('annotation.removeRow', { key: entry.key })"
              @click="removeRow(entry.localId)"
            >
              {{ t('annotation.remove') }}
            </button>
          </td>
        </tr>
      </tbody>
    </table>
    <button type="button" :disabled="!canAdd" @click="addRow">{{ t('annotation.add') }}</button>
    <p v-if="!validation.valid" :id="errorId" class="error-text" role="alert">{{ validation.message }}</p>
    <p v-if="error" class="error-text" role="alert">{{ error }}</p>
  </div>
</template>

<style scoped>
.annotation-editor { display: grid; gap: var(--space-3); }
.annotation-table { width: 100%; border-collapse: collapse; }
.annotation-table th, .annotation-table td { padding: var(--space-2); border: 1px solid var(--color-border); text-align: left; }
.field-input { width: 100%; padding: var(--space-2); border: 1px solid var(--color-border-strong); border-radius: var(--radius-md); }
.field-input:focus-visible { outline: 2px solid var(--color-primary); outline-offset: 2px; box-shadow: var(--focus-ring); }
.scope-field { display: flex; gap: var(--space-2); align-items: center; }
.scope-label { color: var(--color-muted-strong); font-size: var(--font-size-sm); }
.scope-select { padding: var(--space-2); border: 1px solid var(--color-border-strong); border-radius: var(--radius-md); }
.row-remove { color: var(--color-error); }
.hint { margin: 0; color: var(--color-muted); font-size: var(--font-size-sm); }
.error-text { margin: 0; color: var(--color-error); font-size: var(--font-size-sm); }
</style>
