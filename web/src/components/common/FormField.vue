<script setup lang="ts">
import { computed, useId } from 'vue';

/*
 * The one form-field primitive (ux-revamp-plan §9.2, ADR-029 clause 3).
 *
 * It exists because 55 controls each hand-wrote `label + control + error` and the
 * three relations that make a field readable to assistive tech were mostly absent:
 * `<label>` wrapped the control instead of pointing at it with `for`, field errors
 * were plain `<small>` with no `aria-describedby`, and `aria-invalid` appeared
 * twice in the whole console. Those relations are this component's contract:
 *
 *   <label :for="id">  ←→  control :id="id"     (explicit, never implicit wrapping)
 *   aria-describedby = the ids of the messages ACTUALLY rendered (error, then help)
 *   aria-invalid="true" exactly while `error` is non-empty
 *
 * The control stays in the default slot — one primitive has to fit <input>, <select>
 * and <textarea> — so the wiring itself is handed to the caller as slot props. That
 * is the deliberate seam: pass them through and the three relations hold by
 * construction; ignore them and nothing here can save you. `FormField.test.ts`
 * therefore also mounts the real page that adopted this component, so "the caller
 * actually passes them" is asserted and not assumed.
 */
interface Props {
  label: string;
  /** Control id. Generated (useId) when omitted, so `for` can never dangle. */
  id?: string;
  /** Persistent hint. Rendered below the control and referenced by aria-describedby. */
  help?: string;
  /** Field-level error. Rendered with role="alert" and referenced by aria-describedby. */
  error?: string;
  required?: boolean;
  disabled?: boolean;
}

const props = withDefaults(defineProps<Props>(), {
  id: undefined,
  help: '',
  error: '',
  required: false,
  disabled: false,
});

defineSlots<{
  default: (props: {
    id: string;
    /** Space-separated ids of the rendered messages; undefined when there are none. */
    describedBy: string | undefined;
    /** `true` only while an error is shown, so bind it straight to `aria-invalid`. */
    invalid: true | undefined;
    required: boolean;
    disabled: boolean;
  }) => unknown;
}>();

const generatedId = useId();
const fieldId = computed(() => props.id ?? `field-${generatedId}`);
const errorId = computed(() => `${fieldId.value}-error`);
const helpId = computed(() => `${fieldId.value}-help`);
const hasError = computed(() => props.error.trim().length > 0);
const hasHelp = computed(() => props.help.trim().length > 0);

/*
 * An id is listed only while its element is rendered: a describedby pointing at a
 * node that is not in the DOM is worse than no describedby at all. The order
 * mirrors the DOM (error next to the control, help under it).
 */
const describedBy = computed(() => {
  const ids = [hasError.value ? errorId.value : '', hasHelp.value ? helpId.value : ''].filter(Boolean);
  return ids.length > 0 ? ids.join(' ') : undefined;
});
</script>

<template>
  <div class="form-field">
    <label class="form-field__label" :for="fieldId">
      {{ label }}
      <span v-if="required" class="form-field__required" aria-hidden="true">*</span>
    </label>
    <slot
      :id="fieldId"
      :described-by="describedBy"
      :invalid="hasError ? true : undefined"
      :required="required"
      :disabled="disabled"
    />
    <p v-if="hasError" :id="errorId" class="form-field__error" role="alert">{{ error }}</p>
    <p v-if="hasHelp" :id="helpId" class="form-field__help">{{ help }}</p>
  </div>
</template>

<style scoped>
.form-field {
  display: grid;
  gap: var(--space-1);
}

.form-field__label {
  color: var(--color-muted-strong);
  font-size: var(--font-size-md);
  font-weight: var(--font-weight-medium);
}

.form-field__required {
  color: var(--color-error);
}

.form-field__error {
  margin: 0;
  color: var(--color-error-strong);
  font-size: var(--font-size-sm);
}

.form-field__help {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}

/*
 * The control is slotted, so the console's global `:focus-visible` policy (base.css)
 * is restated for it here: a scoped rule cannot reach slot content without
 * `:slotted()`, and a component with its own border-radius/overflow would clip the
 * UA ring anyway. Tokens only — colours come from --color-primary/--focus-ring.
 */
:slotted(input:focus-visible),
:slotted(select:focus-visible),
:slotted(textarea:focus-visible) {
  outline: 2px solid var(--color-primary);
  outline-offset: 2px;
  box-shadow: var(--focus-ring);
}
</style>
