<script setup lang="ts">
import { t } from '@/i18n/messages';
import type { PendingTokenMetadata } from '@/types/operator';
import { formatOperatorTime } from '@/utils/operator-format';

interface Props {
  pending: PendingTokenMetadata;
  canManage: boolean;
  busy: boolean;
}

defineProps<Props>();
const emit = defineEmits<{
  replace: [];
  discard: [];
}>();
</script>

<template>
  <section class="pending" aria-labelledby="pending-token-title">
    <div>
      <p class="eyebrow">{{ t('operator.pending.title') }}</p>
      <h2 id="pending-token-title">{{ t('operator.pending.waiting') }}</h2>
      <p>{{ t('operator.pending.expires') }} {{ formatOperatorTime(pending.expiresAt) }}</p>
      <p v-if="pending.createdByDisplayName">{{ t('operator.pending.createdBy') }} {{ pending.createdByDisplayName }}</p>
    </div>
    <div v-if="canManage" class="actions">
      <button type="button" :disabled="busy" @click="emit('replace')">{{ t('operator.pending.replace') }}</button>
      <button type="button" class="danger" :disabled="busy" @click="emit('discard')">{{ t('operator.pending.revoke') }}</button>
    </div>
  </section>
</template>

<style scoped>
.pending { display: flex; justify-content: space-between; gap: 1rem; padding: 1rem; border: 1px solid var(--color-warning-solid); border-radius: 0.75rem; background: var(--color-warning-surface); }
.pending h2, .pending p { margin: 0; }
.pending p { margin-top: 0.35rem; color: var(--color-warning-ink-deep); }
.eyebrow { font-size: var(--font-size-xs); font-weight: 800; text-transform: uppercase; }
.actions { display: flex; align-items: center; gap: 0.75rem; }
button { padding: 0.55rem 0.8rem; border: 1px solid var(--color-warning-solid-strong); border-radius: 0.375rem; background: var(--color-surface); cursor: pointer; }
button:disabled { cursor: not-allowed; opacity: 0.5; }
.danger { border-color: var(--color-danger-border); color: var(--color-error); }
</style>
