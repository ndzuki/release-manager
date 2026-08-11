<script setup lang="ts">
import { computed, shallowRef } from 'vue';

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
  <div v-if="open" class="disable-dialog" role="dialog" aria-modal="true" aria-labelledby="disable-dialog-title">
    <div class="disable-dialog__panel">
      <h2 id="disable-dialog-title">Disable customer?</h2>
      <p>This action cascades to the customer lifecycle and cannot be undone from this page.</p>
      <ul>
        <li>Enrollment tokens will be revoked.</li>
        <li>Operator certificates will be revoked.</li>
        <li>Active Operator Sessions will be closed.</li>
      </ul>
      <label class="disable-dialog__confirm">
        <input v-model="confirmed" type="checkbox" :disabled="pending">
        I understand the cascading impact.
      </label>
      <div class="disable-dialog__actions">
        <button type="button" :disabled="pending" @click="cancel">Cancel</button>
        <button type="button" class="danger" :disabled="!canConfirm" @click="confirm">
          {{ pending ? 'Disabling…' : 'Confirm disable' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.disable-dialog { position: fixed; inset: 0; z-index: 10; display: grid; place-items: center; padding: 1rem; background: rgb(15 23 42 / 0.5); }
.disable-dialog__panel { display: grid; gap: 0.75rem; width: min(32rem, 100%); padding: 1.25rem; border-radius: 0.75rem; background: #fff; box-shadow: 0 1rem 3rem rgb(15 23 42 / 0.2); }
.disable-dialog__panel h2, .disable-dialog__panel p { margin: 0; }
.disable-dialog__panel ul { display: grid; gap: 0.375rem; margin: 0; padding-left: 1.25rem; }
.disable-dialog__confirm { display: flex; gap: 0.5rem; align-items: flex-start; }
.disable-dialog__actions { display: flex; justify-content: flex-end; gap: 0.5rem; }
.disable-dialog__actions button { padding: 0.5rem 0.75rem; border: 1px solid #94a3b8; border-radius: 0.375rem; background: #fff; cursor: pointer; }
.disable-dialog__actions .danger { border-color: #b91c1c; color: #b91c1c; }
.disable-dialog__actions button:disabled { opacity: 0.55; cursor: not-allowed; }
</style>
