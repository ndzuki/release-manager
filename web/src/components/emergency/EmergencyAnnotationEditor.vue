<script setup lang="ts">
// Annotation batch editor (plan v3 Step 4, AC-058-02/13): rows are bound to
// the server-approved annotation keys only (whitelist), share one scope, use
// stable local IDs, and validate through the pure rules in
// features/emergency/validation.ts.
//
// Contract divergence (recorded): the canonical ExecuteEmergencyChange does
// not carry annotation entries yet, so this editor validates and previews but
// the page does not submit annotation intents until the upstream contract
// extends (no frontend simulation of backend state).
import { computed, ref, watch } from 'vue';
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
          <th scope="col">Key（白名单）</th>
          <th scope="col">Value</th>
          <th scope="col">Scope</th>
          <th scope="col"></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="entry in drafts" :key="entry.localId">
          <td>
            <select
              class="field-input"
              :value="entry.key"
              @change="updateRow(entry.localId, { key: ($event.target as HTMLSelectElement).value })"
            >
              <option v-for="key in approvedKeys" :key="key" :value="key">{{ key }}</option>
            </select>
          </td>
          <td>
            <input
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
    <button type="button" :disabled="!canAdd" @click="addRow">添加注解</button>
    <p v-if="!validation.valid" class="error-text">{{ validation.message }}</p>
  </div>
</template>

<style scoped>
.annotation-editor { display: grid; gap: 0.75rem; }
.annotation-table { width: 100%; border-collapse: collapse; }
.annotation-table th, .annotation-table td { padding: 0.4rem 0.5rem; border: 1px solid #e2e8f0; text-align: left; }
.field-input { width: 100%; padding: 0.4rem; border: 1px solid #cbd5e1; border-radius: 0.375rem; }
.row-remove { color: #b91c1c; }
.hint { color: #64748b; }
.error-text { color: #b91c1c; }
</style>
