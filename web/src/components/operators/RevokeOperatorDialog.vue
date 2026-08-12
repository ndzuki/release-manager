<script setup lang="ts">
import { computed, shallowRef } from 'vue';
import { validateRevokeReason } from '@/utils/operator-validation';

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

function submit(): void {
  if (violation.value) return;
  emit('confirm', reason.value.trim());
}
</script>

<template>
  <div class="backdrop" role="presentation">
    <section class="dialog" role="dialog" aria-modal="true" aria-labelledby="revoke-title">
      <h2 id="revoke-title">Revoke {{ operatorName }}</h2>
      <p>This immediately revokes active sessions and cannot be undone.</p>
      <label>
        Reason
        <textarea v-model="reason" rows="4" maxlength="500" />
      </label>
      <p v-if="violation" class="error">{{ violation.description }}</p>
      <p v-if="errorMessage" class="error" role="alert">{{ errorMessage }}</p>
      <div class="actions">
        <button type="button" :disabled="submitting" @click="emit('cancel')">Cancel</button>
        <button type="button" class="danger" :disabled="submitting || Boolean(violation)" @click="submit">
          {{ submitting ? 'Revoking…' : 'Confirm revoke' }}
        </button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.backdrop { position: fixed; inset: 0; z-index: 20; display: grid; place-items: center; padding: 1rem; background: rgb(15 23 42 / 0.72); }
.dialog { display: grid; width: min(32rem, 100%); gap: 1rem; padding: 1.5rem; border-radius: 0.75rem; background: #fff; }
.dialog h2, .dialog p { margin: 0; }
.dialog label { display: grid; gap: 0.35rem; font-weight: 700; }
textarea { padding: 0.65rem; border: 1px solid #94a3b8; border-radius: 0.375rem; font: inherit; }
.actions { display: flex; justify-content: flex-end; gap: 0.75rem; }
button { padding: 0.55rem 0.8rem; border: 1px solid #94a3b8; border-radius: 0.375rem; background: #fff; cursor: pointer; }
.danger { border-color: #ef4444; color: #b91c1c; }
.error { color: #b91c1c; }
</style>