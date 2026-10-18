<script setup lang="ts">
import AppDialog from '@/components/common/AppDialog.vue';
import { t } from '@/i18n/messages';

/*
 * TASK-363: the disable-organization confirmation, carved out of OrganizationPage.
 *
 * Disabling is destructive and terminal, so the confirmation is a `danger`
 * alertdialog and the RPC runs only from `confirm` — never from the header. The page
 * renders this only while an organization is armed and keeps the failure inside the
 * still-open dialog, because the page-level alert is behind the backdrop.
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
    :title="t('organization.disable.title')"
    :description="description"
    danger
    :close-on-backdrop="false"
    :close-on-escape="!saving"
    @close="emit('close')"
  >
    <p v-if="failure" class="organization__error" role="alert" data-testid="organization-disable-error">
      {{ failure }}
    </p>
    <template #footer>
      <button type="button" :disabled="saving" @click="emit('close')">
        {{ t('action.cancel') }}
      </button>
      <button
        type="button"
        class="organization__confirm-danger"
        :disabled="saving"
        data-testid="organization-disable-confirm"
        @click="emit('confirm')"
      >
        {{ saving ? t('organization.disable.submitting') : t('organization.disable.confirm') }}
      </button>
    </template>
  </AppDialog>
</template>

<style scoped>
/* AppDialog's footer supplies the baseline; the danger tone is the caller's rule. */
.organization__confirm-danger {
  border-color: var(--color-danger-border);
  color: var(--color-error);
}

.organization__error {
  margin: 0;
  color: var(--color-error);
  font-size: var(--font-size-sm);
}
</style>
