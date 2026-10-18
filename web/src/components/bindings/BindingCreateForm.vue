<script setup lang="ts">
/*
 * TASK-363: the "new binding" form, carved out of BindingsPage unchanged.
 *
 * The page owns the form state (composables/useBindingCreate) through the
 * `customerId` model and decides what submitting means; this component owns only
 * the markup. The root element is still the original
 * `<form class="governance__panel">`.
 */
defineProps<{
  saving: boolean;
  error: string;
}>();

const emit = defineEmits<{
  submit: [];
}>();

const customerId = defineModel<string>('customerId', { required: true });
</script>

<template>
  <form class="governance__panel" @submit.prevent="emit('submit')">
    <h2>新增绑定</h2>
    <label>
      客户 ID
      <input v-model="customerId" name="customerId" placeholder="客户 ID" />
    </label>
    <button type="submit" :disabled="saving">{{ saving ? '提交中…' : '绑定' }}</button>
    <p v-if="error" class="governance__error" role="alert" data-testid="binding-create-error">{{ error }}</p>
  </form>
</template>

<style scoped>
.governance__panel {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

.governance__panel h2 {
  margin: 0;
  font-size: var(--font-size-lg);
}

.governance__panel label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.governance__panel input {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.governance__panel button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.governance__error {
  margin: 0;
  color: var(--color-error);
  font-size: var(--font-size-sm);
}
</style>
