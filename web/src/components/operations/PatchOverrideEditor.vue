<script setup lang="ts">
import type { PatchOverride } from '@/types/operation';

const model = defineModel<PatchOverride[]>({ required: true });
const props = defineProps<{ error?: string; errorIndex?: number }>();

function addRow(): void {
  model.value.push({ path: '', value: '', kind: 'LITERAL' });
}

function removeRow(index: number): void {
  model.value.splice(index, 1);
}
</script>

<template>
  <fieldset class="patch-editor">
    <legend>Patch 覆盖</legend>
    <p class="patch-editor__hint">只允许 dot-path。Secret 类字段必须使用 Secret 引用，不保存明文。</p>
    <div
      v-for="(override, index) in model"
      :key="index"
      class="patch-editor__row"
      :class="{ 'patch-editor__row--error': props.errorIndex === index }"
    >
      <input
        v-model.trim="override.path"
        :aria-label="`Patch ${index + 1} path`"
        :aria-invalid="props.errorIndex === index"
        placeholder="image.tag"
      />
      <select v-model="override.kind" :aria-label="`Patch ${index + 1} kind`">
        <option value="LITERAL">Literal</option>
        <option value="SECRET_REF">Secret ref</option>
      </select>
      <input
        v-model="override.value"
        :aria-label="`Patch ${index + 1} value`"
        :aria-invalid="props.errorIndex === index"
        :placeholder="override.kind === 'SECRET_REF' ? 'secret-name' : 'value'"
      />
      <button type="button" class="patch-editor__remove" @click="removeRow(index)">移除</button>
      <p v-if="props.error && props.errorIndex === index" class="patch-editor__row-error" role="alert">
        {{ props.error }}
      </p>
    </div>
    <button type="button" class="patch-editor__add" @click="addRow">添加 Patch</button>
  </fieldset>
</template>

<style scoped>
.patch-editor { display: grid; gap: 0.75rem; padding: 1rem; border: 1px solid #cbd5e1; border-radius: 0.75rem; }
.patch-editor__hint { margin: 0; color: #64748b; font-size: 0.875rem; }
.patch-editor__row { display: grid; grid-template-columns: 2fr 1fr 2fr auto; gap: 0.5rem; }
.patch-editor__row--error { padding: 0.65rem; border: 1px solid #dc2626; border-radius: 0.5rem; background: #fef2f2; }
.patch-editor__row-error { grid-column: 1 / -1; margin: 0; color: #b91c1c; }
.patch-editor input, .patch-editor select { min-width: 0; padding: 0.6rem; border: 1px solid #94a3b8; border-radius: 0.375rem; }
.patch-editor__add, .patch-editor__remove { width: fit-content; padding: 0.55rem 0.75rem; border: 1px solid #94a3b8; border-radius: 0.375rem; background: #fff; }
.patch-editor__remove { color: #b91c1c; }
@media (max-width: 48rem) { .patch-editor__row { grid-template-columns: 1fr; } }
</style>
