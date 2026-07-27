<script setup lang="ts">
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
      <p class="eyebrow">Pending enrollment token</p>
      <h2 id="pending-token-title">A token is waiting to be used</h2>
      <p>Expires {{ formatOperatorTime(pending.expiresAt) }}</p>
      <p v-if="pending.createdByDisplayName">Created by {{ pending.createdByDisplayName }}</p>
    </div>
    <div v-if="canManage" class="actions">
      <button type="button" :disabled="busy" @click="emit('replace')">Replace token</button>
      <button type="button" class="danger" :disabled="busy" @click="emit('discard')">Revoke pending token</button>
    </div>
  </section>
</template>

<style scoped>
.pending { display: flex; justify-content: space-between; gap: 1rem; padding: 1rem; border: 1px solid #f59e0b; border-radius: 0.75rem; background: #fffbeb; }
.pending h2, .pending p { margin: 0; }
.pending p { margin-top: 0.35rem; color: #78350f; }
.eyebrow { font-size: 0.75rem; font-weight: 800; text-transform: uppercase; }
.actions { display: flex; align-items: center; gap: 0.75rem; }
button { padding: 0.55rem 0.8rem; border: 1px solid #d97706; border-radius: 0.375rem; background: #fff; cursor: pointer; }
button:disabled { cursor: not-allowed; opacity: 0.5; }
.danger { border-color: #ef4444; color: #b91c1c; }
</style>
