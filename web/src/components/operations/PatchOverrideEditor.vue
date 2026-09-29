<script setup lang="ts">
import { t } from '@/i18n/messages';
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
    <legend>{{ t('operation.patch.title') }}</legend>
    <p class="patch-editor__hint">{{ t('operation.patch.help') }}</p>
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
        <option value="LITERAL">{{ t('operation.patch.literal') }}</option>
        <option value="SECRET_REF">{{ t('operation.patch.secretRef') }}</option>
      </select>
      <input
        v-model="override.value"
        :aria-label="`Patch ${index + 1} value`"
        :aria-invalid="props.errorIndex === index"
        :placeholder="override.kind === 'SECRET_REF' ? 'secret-name' : 'value'"
      />
      <button type="button" class="patch-editor__remove" @click="removeRow(index)">{{ t('cluster.rules.remove') }}</button>
      <p v-if="props.error && props.errorIndex === index" class="patch-editor__row-error" role="alert">
        {{ props.error }}
      </p>
    </div>
    <button type="button" class="patch-editor__add" @click="addRow">{{ t('operation.patch.add') }}</button>
  </fieldset>
</template>

<style scoped>
.patch-editor { display: grid; gap: 0.75rem; padding: 1rem; border: 1px solid var(--color-border-strong); border-radius: 0.75rem; }
.patch-editor__hint { margin: 0; color: var(--color-muted); font-size: var(--font-size-md); }
.patch-editor__row { display: grid; grid-template-columns: 2fr 1fr 2fr auto; gap: 0.5rem; }
.patch-editor__row--error { padding: 0.65rem; border: 1px solid var(--color-danger); border-radius: 0.5rem; background: var(--color-danger-soft); }
.patch-editor__row-error { grid-column: 1 / -1; margin: 0; color: var(--color-error); }
.patch-editor input, .patch-editor select { min-width: 0; padding: 0.6rem; border: 1px solid var(--color-subtle); border-radius: 0.375rem; }
.patch-editor__add, .patch-editor__remove { width: fit-content; padding: 0.55rem 0.75rem; border: 1px solid var(--color-subtle); border-radius: 0.375rem; background: var(--color-surface); }
.patch-editor__remove { color: var(--color-error); }
@media (max-width: 48rem) { .patch-editor__row { grid-template-columns: 1fr; } }
</style>
