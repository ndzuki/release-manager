<script setup lang="ts">
import { computed, shallowRef } from 'vue';

const props = defineProps<{ submitting?: boolean }>();
const emit = defineEmits<{ submit: [reason: string]; close: [] }>();
const reason = shallowRef('');
const error = computed(() => reason.value.length > 1000 ? '拒绝原因过长 (上限 1000 字符)' : '');

function submit(): void {
  if (error.value || props.submitting) return;
  emit('submit', reason.value.trim());
}
</script>

<template>
  <div class="dialog-backdrop" role="presentation" @click.self="emit('close')">
    <section class="dialog" role="dialog" aria-modal="true" aria-labelledby="reject-revision-title">
      <h2 id="reject-revision-title">拒绝 ValuesRevision</h2>
      <label>
        拒绝原因（可选）
        <textarea v-model="reason" rows="6" maxlength="1001" placeholder="说明需要修改的内容" />
      </label>
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <div class="dialog__actions">
        <button type="button" @click="emit('close')">取消</button>
        <button type="button" class="danger" :disabled="Boolean(error) || submitting" @click="submit">
          {{ submitting ? '提交中…' : '确认拒绝' }}
        </button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.dialog-backdrop { position: fixed; inset: 0; z-index: 20; display: grid; place-items: center; padding: 1rem; background: #0f172a99; }
.dialog { display: grid; width: min(32rem, 100%); gap: 1rem; padding: 1.25rem; border-radius: 0.75rem; background: #fff; box-shadow: 0 1.5rem 4rem #0f172a55; }
h2, p { margin: 0; }
label { display: grid; gap: 0.4rem; color: #334155; font-size: 0.85rem; font-weight: 700; }
textarea { padding: 0.75rem; border: 1px solid #cbd5e1; border-radius: 0.45rem; resize: vertical; font: inherit; }
.error { color: #b91c1c; font-size: 0.8rem; }
.dialog__actions { display: flex; justify-content: flex-end; gap: 0.75rem; }
button { min-height: 2.5rem; padding: 0.45rem 0.75rem; border: 1px solid #cbd5e1; border-radius: 0.45rem; background: #fff; cursor: pointer; }
button.danger { border-color: #b91c1c; background: #b91c1c; color: #fff; }
button:disabled { cursor: not-allowed; opacity: 0.6; }
</style>
