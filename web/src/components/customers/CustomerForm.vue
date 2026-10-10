<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed } from 'vue';
import FormField from '@/components/common/FormField.vue';
import type { CustomerFormInput, FieldViolation } from '@/types/customer';

const props = withDefaults(defineProps<{
  modelValue: CustomerFormInput;
  readonly?: boolean;
  submitting?: boolean;
  submitLabel?: string;
  fieldViolations?: FieldViolation[];
}>(), {
  readonly: false,
  submitting: false,
  submitLabel: t('customer.form.save'),
  fieldViolations: () => [],
});

const emit = defineEmits<{
  'update:modelValue': [value: CustomerFormInput];
  submit: [];
}>();

const nameError = computed(() => props.fieldViolations.find((item) => item.field === 'name')?.description ?? '');
const slugError = computed(() => props.fieldViolations.find((item) => item.field === 'slug')?.description ?? '');
const canSubmit = computed(() => !props.readonly && !props.submitting && props.modelValue.name.trim().length > 0);

function updateField(field: 'name' | 'slug', value: string) {
  emit('update:modelValue', { ...props.modelValue, [field]: value });
}
</script>

<template>
  <form class="customer-form" @submit.prevent="emit('submit')">
    <!--
     TASK-271: the errors used to live INSIDE an implicitly wrapping <label>, so the
     text was part of the control's accessible NAME and an aria-describedby pointing at
     the same node would announce it twice. FormField (TASK-268) owns the clean shape
     instead: `label for` + slotted control + `aria-describedby` on the rendered
     message + `role="alert"` on the error + `aria-invalid` only while there is one.
    -->
    <FormField :label="t('customer.form.name')" :error="nameError" :disabled="readonly || submitting">
      <template #default="{ id, describedBy, invalid, disabled }">
        <input
          :id="id"
          :value="modelValue.name"
          type="text"
          autocomplete="organization"
          maxlength="253"
          :disabled="disabled"
          :aria-describedby="describedBy"
          :aria-invalid="invalid"
          @input="updateField('name', ($event.target as HTMLInputElement).value)"
        >
      </template>
    </FormField>

    <FormField :label="t('customer.form.slug')" :error="slugError" :disabled="readonly || submitting">
      <template #default="{ id, describedBy, invalid, disabled }">
        <input
          :id="id"
          :value="modelValue.slug"
          type="text"
          autocomplete="off"
          maxlength="253"
          :disabled="disabled"
          :aria-describedby="describedBy"
          :aria-invalid="invalid"
          @input="updateField('slug', ($event.target as HTMLInputElement).value)"
        >
      </template>
    </FormField>

    <p v-if="readonly" class="customer-form__readonly">{{ t('customer.form.readonly') }}</p>
    <button v-else class="customer-form__submit" type="submit" :disabled="!canSubmit">
      {{ submitting ? t('customer.form.saving') : submitLabel }}
    </button>
  </form>
</template>

<style scoped>
.customer-form { display: grid; gap: 1rem; }
/*
 * The controls are slotted into FormField, and slot content is compiled in THIS
 * component's scope, so a scoped `input` rule still reaches them. The field
 * chrome (label / error / spacing) belongs to FormField now.
 */
.customer-form input { padding: 0.625rem 0.75rem; border: 1px solid var(--color-subtle); border-radius: 0.375rem; font: inherit; }
.customer-form input:disabled { background: var(--color-surface-muted); color: var(--color-muted-strong); }
.customer-form__readonly { margin: 0; color: var(--color-muted); }
.customer-form__submit { justify-self: start; padding: 0.625rem 0.875rem; border: 0; border-radius: 0.375rem; background: var(--color-primary); color: var(--color-on-accent); font-weight: 700; cursor: pointer; }
.customer-form__submit:disabled { opacity: 0.55; cursor: not-allowed; }
</style>
