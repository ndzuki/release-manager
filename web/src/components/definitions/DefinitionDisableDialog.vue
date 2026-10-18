<script setup lang="ts">
import { t } from '@/i18n/messages';
import AppDialog from '@/components/common/AppDialog.vue';

/*
 * TASK-363: the disable confirmation, carved out of DefinitionsPage.
 *
 * Destructive and terminal, so the confirmation is a `danger` alertdialog and the
 * destructive RPC runs only from `confirm` — never from the row button. The page
 * renders this only while a definition is armed; the failure stays inside the
 * still-open dialog because the page-level <ErrorState> is behind the backdrop.
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
    :title="t('definitions.disable.title')"
    :description="description"
    danger
    :close-on-backdrop="false"
    :close-on-escape="!saving"
    @close="emit('close')"
  >
    <p v-if="failure" class="definitions__error" role="alert" data-testid="definitions-disable-error">
      {{ failure }}
    </p>
    <template #footer>
      <button type="button" :disabled="saving" @click="emit('close')">
        {{ t('action.cancel') }}
      </button>
      <button
        type="button"
        class="definitions__confirm-danger"
        :disabled="saving"
        data-testid="definitions-disable-confirm"
        @click="emit('confirm')"
      >
        {{ saving ? t('definitions.disable.submitting') : t('definitions.disable.confirm') }}
      </button>
    </template>
  </AppDialog>
</template>

<style scoped>
/* AppDialog's footer supplies a baseline for slotted buttons; the danger tone is the
   caller's rule (same split as BindingsPage's confirm). */
.definitions__confirm-danger {
  border-color: var(--color-danger-border);
  color: var(--color-error);
}

.definitions__error {
  margin: 0;
  color: var(--color-error);
  font-size: var(--font-size-sm);
}
</style>
