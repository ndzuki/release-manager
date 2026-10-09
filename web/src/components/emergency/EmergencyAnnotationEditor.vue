<script setup lang="ts">
import { t } from '@/i18n/messages';
// Annotation batch editor (plan v3 Step 4, AC-058-02/13): rows are bound to
// the server-approved annotation keys only (whitelist), share one scope, use
// stable local IDs, and validate through the pure rules in
// features/emergency/validation.ts.
//
// Contract divergence (recorded): the canonical ExecuteEmergencyChange does
// not carry annotation entries yet, so this editor validates and previews but
// the page does not submit annotation intents until the upstream contract
// extends (no frontend simulation of backend state).
import { computed, ref, useId, watch } from 'vue';
import { validateAnnotationEntries, type AnnotationEntryDraft } from '@/features/emergency/validation';

const props = defineProps<{
  /** Server-approved annotation keys (whitelist from the target projection). */
  approvedKeys: string[];
  scope: string;
  values: Array<{ localId: string; key: string; value: string; scope: string }>;
}>();

const emit = defineEmits<{ update: [entries: AnnotationEntryDraft[]] }>();

const nextLocalId = ref(1);

const drafts = computed<AnnotationEntryDraft[]>(() => props.values);

const validation = computed(() => validateAnnotationEntries(drafts.value));

const canAdd = computed(() => props.approvedKeys.length > 0 && drafts.value.length < 50);

/*
 * Ids for the per-row key/value controls. Prefix from useId() so two editors on one
 * page cannot produce the same id, and stable across re-renders. The row cell's text
 * column header (Key/值) does not name the control, so each control owns a
 * visually-hidden <label for> carrying the column name plus the row number.
 */
const rowIdPrefix = useId();

function rowFieldId(localId: string, field: 'key' | 'value'): string {
  return `${rowIdPrefix}-annotation-${localId}-${field}`;
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
    <p class="hint">注解变更当前后端契约暂不支持提交，此处仅展示白名单与批量校验。</p>
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
              @change="updateRow(entry.localId, { key: ($event.target as HTMLSelectElement).value })"
            >
              <option v-for="key in approvedKeys" :key="key" :value="key">{{ key }}</option>
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
              :placeholder="`1–2048 UTF-8 字节`"
              @input="updateRow(entry.localId, { value: ($event.target as HTMLInputElement).value })"
            />
          </td>
          <td>{{ entry.scope }}</td>
          <td>
            <button type="button" class="row-remove" :aria-label="`移除注解 ${entry.key}`" @click="removeRow(entry.localId)">
              移除
            </button>
          </td>
        </tr>
      </tbody>
    </table>
    <button type="button" :disabled="!canAdd" @click="addRow">{{ t('annotation.add') }}</button>
    <p v-if="!validation.valid" class="error-text">{{ validation.message }}</p>
  </div>
</template>

<style scoped>
.annotation-editor { display: grid; gap: 0.75rem; }
.annotation-table { width: 100%; border-collapse: collapse; }
.annotation-table th, .annotation-table td { padding: 0.4rem 0.5rem; border: 1px solid var(--color-border); text-align: left; }
.field-input { width: 100%; padding: 0.4rem; border: 1px solid var(--color-border-strong); border-radius: 0.375rem; }
.row-remove { color: var(--color-error); }
.hint { color: var(--color-muted); }
.error-text { color: var(--color-error); }
</style>
