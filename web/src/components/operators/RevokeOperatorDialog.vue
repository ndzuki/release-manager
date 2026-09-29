<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, shallowRef, useId } from 'vue';
import { validateRevokeReason } from '@/utils/operator-validation';
import AppDialog from '@/components/common/AppDialog.vue';

interface Props {
  operatorName: string;
  submitting: boolean;
  errorMessage?: string;
}

defineProps<Props>();
const emit = defineEmits<{
  cancel: [];
  confirm: [reason: string];
}>();
const reason = shallowRef('');
const violation = computed(() => validateRevokeReason(reason.value));
const violationId = useId();

function submit(): void {
  if (violation.value) return;
  emit('confirm', reason.value.trim());
}
</script>

<template>
  <!-- Rendered only while the parent wants it open, so `open` is always true;
       the reason textarea makes a backdrop-click close actively harmful. -->
  <AppDialog
    :open="true"
    :title="`${t('operator.table.revoke')} ${operatorName}`"
    :description="t('operator.revoke.warning')"
    danger
    :close-on-backdrop="false"
    :close-on-escape="!submitting"
    @close="emit('cancel')"
  >
    <label class="revoke-reason">
      {{ t('operator.revoke.reason') }}
      <textarea
        v-model="reason"
        rows="4"
        maxlength="500"
        :aria-invalid="Boolean(violation)"
        :aria-describedby="violation ? violationId : undefined"
      />
    </label>
    <p v-if="violation" :id="violationId" class="revoke-reason__error" role="alert">
      {{ violation.description }}
    </p>
    <p v-if="errorMessage" class="revoke-reason__error" role="alert">{{ errorMessage }}</p>

    <template #footer>
      <button type="button" :disabled="submitting" @click="emit('cancel')">{{ t('action.cancel') }}</button>
      <button
        type="button"
        class="revoke-reason__danger"
        :disabled="submitting || Boolean(violation)"
        @click="submit"
      >
        {{ submitting ? t('operator.revoke.inProgress') : t('operator.revoke.confirm') }}
      </button>
    </template>
  </AppDialog>
</template>

<style scoped>
.revoke-reason {
  display: grid;
  gap: var(--space-1);
  font-weight: var(--font-weight-bold);
}

.revoke-reason textarea {
  padding: 0.65rem; /* HEAD value; no 4px-scale step */
  border: 1px solid var(--color-subtle);
  border-radius: var(--radius-md);
  font: inherit;
  font-weight: var(--font-weight-regular);
}

.revoke-reason textarea[aria-invalid='true'] {
  border-color: var(--color-error);
}

.revoke-reason__error {
  margin: 0;
  color: var(--color-error);
}

.revoke-reason__danger {
  border-color: var(--color-danger-border);
  color: var(--color-error);
}
</style>
