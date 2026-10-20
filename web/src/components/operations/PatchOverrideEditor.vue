<script setup lang="ts">
import { t } from '@/i18n/messages';
import type { PatchOverride } from '@/types/operation';

const model = defineModel<PatchOverride[]>({ required: true });
const props = defineProps<{ error?: string; errorIndex?: number }>();
// W12 (UX-053): the editor owns the patch controls, so it is the component that
// knows when the operator leaves one of them; the form decides what to validate.
const emit = defineEmits<{ blur: [] }>();

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
    <!--
      W12 (UX-024): a patch error that names no row used to render nowhere — every
      message was gated on `errorIndex === index`, so an error without an index became
      invisible. It now gets a fieldset-level summary, which is also the focus target
      when the form moves focus to the first error.
    -->
    <p
      v-if="props.error && props.errorIndex === undefined"
      class="patch-editor__error"
      role="alert"
      data-field-error
      tabindex="-1"
    >{{ props.error }}</p>
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
        @blur="emit('blur')"
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
        @blur="emit('blur')"
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
/*
 * Direction C (TASK-394): the editor is a nested fieldset on the form card, so it uses
 * the palette's surfaces and hairline instead of the legacy strong border. The grid is
 * `minmax(0, …)` on every track: the automatic minimum of an input is its intrinsic
 * width, which is exactly how the old layout pushed the page past 375px.
 */
.patch-editor {
  display: grid;
  gap: 10px;
  min-width: 0;
  padding: 14px;
  border: 1px solid var(--bd);
  border-radius: 6px;
}

.patch-editor__hint {
  margin: 0;
  color: var(--mu);
  font-size: var(--font-size-sm);
}

.patch-editor__error {
  margin: 0;
  color: var(--er);
  font-size: var(--font-size-sm);
}

.patch-editor__row {
  display: grid;
  grid-template-columns: minmax(0, 2fr) minmax(0, 1fr) minmax(0, 2fr) auto;
  gap: 8px;
  min-width: 0;
}

.patch-editor__row--error {
  padding: 10px;
  border: 1px solid var(--color-danger-border);
  border-radius: var(--r);
  background: var(--color-danger-soft);
}

.patch-editor__row-error {
  grid-column: 1 / -1;
  margin: 0;
  color: var(--er);
  font-size: var(--font-size-sm);
}

.patch-editor input,
.patch-editor select {
  min-width: 0;
  padding: 8px 10px;
  border: 1px solid var(--bd);
  border-radius: 6px;
  background: var(--bg);
  color: var(--tx);
}

.patch-editor input::placeholder {
  color: var(--mu);
}

.patch-editor__add,
.patch-editor__remove {
  width: fit-content;
  padding: 7px 10px;
  border: 1px solid var(--bd);
  border-radius: 6px;
  background: var(--sf2);
  color: var(--tx);
  cursor: pointer;
}

.patch-editor__remove {
  color: var(--er);
}

@media (max-width: 48rem) {
  .patch-editor__row {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
