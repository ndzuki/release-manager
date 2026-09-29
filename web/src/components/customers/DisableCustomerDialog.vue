<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, shallowRef } from 'vue';
import AppDialog from '@/components/common/AppDialog.vue';

const props = withDefaults(defineProps<{
  open?: boolean;
  pending?: boolean;
}>(), {
  open: false,
  pending: false,
});

const emit = defineEmits<{
  confirm: [];
  cancel: [];
}>();

const confirmed = shallowRef(false);
const canConfirm = computed(() => confirmed.value && !props.pending);

function cancel() {
  if (props.pending) return;
  confirmed.value = false;
  emit('cancel');
}

function confirm() {
  if (!canConfirm.value) return;
  emit('confirm');
}
</script>

<template>
  <!-- Destructive + irreversible: the backdrop must not dismiss it, and Escape is
       disabled while the disable request is in flight. -->
  <AppDialog
    :open="open"
    :title="t('customer.disable.title')"
    :description="t('customer.disable.body')"
    danger
    :close-on-backdrop="false"
    :close-on-escape="!pending"
    @close="cancel"
  >
    <ul class="disable-dialog__impact">
      <li>{{ t('customer.disable.cascadeTokens') }}</li>
      <li>{{ t('customer.disable.cascadeCerts') }}</li>
      <li>{{ t('customer.disable.cascadeSessions') }}</li>
    </ul>
    <label class="disable-dialog__confirm">
      <input v-model="confirmed" type="checkbox" :disabled="pending">
      {{ t('customer.disable.acknowledge') }}
    </label>

    <template #footer>
      <button type="button" :disabled="pending" @click="cancel">{{ t('action.cancel') }}</button>
      <button type="button" class="disable-dialog__danger" :disabled="!canConfirm" @click="confirm">
        {{ pending ? t('customer.disable.inProgress') : t('customer.disable.confirm') }}
      </button>
    </template>
  </AppDialog>
</template>

<style scoped>
.disable-dialog__impact {
  display: grid;
  gap: var(--space-1);
  margin: 0;
  padding-left: 1.25rem; /* HEAD value; no 4px-scale step */
}

.disable-dialog__confirm {
  display: flex;
  gap: var(--space-2);
  align-items: flex-start;
}

.disable-dialog__danger {
  border-color: var(--color-error);
  color: var(--color-error);
}
</style>
