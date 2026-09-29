<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed } from 'vue';
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
    <label class="customer-form__field">
      <span>{{ t('customer.form.name') }}</span>
      <input
        :value="modelValue.name"
        type="text"
        autocomplete="organization"
        maxlength="253"
        :disabled="readonly || submitting"
        :aria-invalid="Boolean(nameError)"
        @input="updateField('name', ($event.target as HTMLInputElement).value)"
      >
      <small v-if="nameError" class="customer-form__error">{{ nameError }}</small>
    </label>

    <label class="customer-form__field">
      <span>{{ t('customer.form.slug') }}</span>
      <input
        :value="modelValue.slug"
        type="text"
        autocomplete="off"
        maxlength="253"
        :disabled="readonly || submitting"
        :aria-invalid="Boolean(slugError)"
        @input="updateField('slug', ($event.target as HTMLInputElement).value)"
      >
      <small v-if="slugError" class="customer-form__error">{{ slugError }}</small>
    </label>

    <p v-if="readonly" class="customer-form__readonly">{{ t('customer.form.readonly') }}</p>
    <button v-else class="customer-form__submit" type="submit" :disabled="!canSubmit">
      {{ submitting ? t('customer.form.saving') : submitLabel }}
    </button>
  </form>
</template>

<style scoped>
.customer-form { display: grid; gap: 1rem; }
.customer-form__field { display: grid; gap: 0.375rem; font-weight: 600; }
.customer-form__field input { padding: 0.625rem 0.75rem; border: 1px solid var(--color-subtle); border-radius: 0.375rem; font: inherit; }
.customer-form__field input:disabled { background: var(--color-surface-muted); color: var(--color-muted-strong); }
.customer-form__error { color: var(--color-error); font-weight: 500; }
.customer-form__readonly { margin: 0; color: var(--color-muted); }
.customer-form__submit { justify-self: start; padding: 0.625rem 0.875rem; border: 0; border-radius: 0.375rem; background: var(--color-primary); color: var(--color-on-accent); font-weight: 700; cursor: pointer; }
.customer-form__submit:disabled { opacity: 0.55; cursor: not-allowed; }
</style>
