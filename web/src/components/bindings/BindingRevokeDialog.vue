<script setup lang="ts">
import AppDialog from '@/components/common/AppDialog.vue';

/*
 * TASK-363: the revoke confirmation, carved out of BindingsPage.
 *
 * Dangerous and irreversible, so the confirmation is a `danger` alertdialog and
 * the destructive RPC runs only from `confirm` — never from the row button. The
 * page renders this only while a binding is armed; the failure stays inside the
 * still-open dialog because the page-level <ErrorState> is behind the backdrop
 * (TASK-297/TASK-303). `tabindex="-1"` on the notice the page focuses lives in
 * the page, not here.
 */
defineProps<{
  description: string;
  failure: string | null;
  saving: boolean;
}>();

const emit = defineEmits<{
  close: [];
  confirm: [];
}>();
</script>

<template>
  <AppDialog
    :open="true"
    title="撤销绑定"
    :description="description"
    danger
    :close-on-backdrop="false"
    :close-on-escape="!saving"
    @close="emit('close')"
  >
    <p v-if="failure" class="governance__error" role="alert" data-testid="revoke-failure">
      {{ failure }}
    </p>
    <template #footer>
      <button type="button" :disabled="saving" @click="emit('close')">取消</button>
      <button
        type="button"
        class="governance__confirm-danger"
        :disabled="saving"
        @click="emit('confirm')"
      >
        {{ saving ? '撤销中…' : '确认撤销' }}
      </button>
    </template>
  </AppDialog>
</template>

<style scoped>
/* The confirm button is slotted into AppDialog's footer, which only supplies a
   baseline; the danger tone is the caller's rule (same split as
   RevokeOperatorDialog). */
.governance__confirm-danger {
  border-color: var(--color-danger-border);
  color: var(--color-error-strong);
}

.governance__error {
  margin: 0;
  color: var(--color-error-strong);
  font-size: var(--font-size-sm);
}
</style>
