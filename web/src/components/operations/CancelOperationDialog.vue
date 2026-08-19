<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { REASON_MAX_CHARS, REASON_MIN_CHARS } from '@/stores/operationTimeline';

const props = defineProps<{
  submitting?: boolean;
  error?: { code: string; message: string } | null;
  emergencyQueued?: boolean;
}>();

const emit = defineEmits<{ submit: [reason: string]; close: [] }>();

const reason = ref('');
const input = ref<HTMLTextAreaElement | null>(null);
const touched = ref(false);

const charCount = computed(() => [...reason.value.trim()].length);
const lengthError = computed(() => {
  if (charCount.value < REASON_MIN_CHARS) return '取消原因不能为空';
  if (charCount.value > REASON_MAX_CHARS) return `取消原因过长（上限 ${REASON_MAX_CHARS} 字符）`;
  return '';
});
const validationError = computed(() => (touched.value ? lengthError.value : ''));

onMounted(() => {
  input.value?.focus();
});

function submit(): void {
  touched.value = true;
  if (lengthError.value || props.submitting) return;
  emit('submit', reason.value.trim());
}
</script>

<template>
  <div class="dialog-backdrop" role="presentation" @click.self="emit('close')">
    <section
      class="dialog"
      role="dialog"
      aria-modal="true"
      aria-labelledby="cancel-operation-title"
      @keydown.esc="emit('close')"
    >
      <h2 id="cancel-operation-title">取消发布操作</h2>
      <p v-if="emergencyQueued" class="dialog__emergency-note">取消不等于 K8s 回滚，集群中的紧急变更可能仍会生效。</p>

      <label class="dialog__label">
        取消原因（必填）
        <textarea
          ref="input"
          v-model="reason"
          rows="5"
          :maxlength="REASON_MAX_CHARS + 20"
          :aria-invalid="Boolean(validationError)"
          aria-describedby="cancel-reason-help cancel-reason-error"
          placeholder="说明取消原因"
        />
        <span id="cancel-reason-help" class="dialog__count">{{ charCount }}/{{ REASON_MAX_CHARS }}</span>
      </label>

      <p v-if="validationError" id="cancel-reason-error" class="dialog__error" role="alert">{{ validationError }}</p>
      <p v-else-if="error" class="dialog__error" role="alert">
        {{ error.message }}<template v-if="error.code">（{{ error.code }}）</template>
      </p>

      <div class="dialog__actions">
        <button type="button" :disabled="submitting" @click="emit('close')">返回</button>
        <button type="button" class="danger" :disabled="submitting" @click="submit">
          {{ submitting ? '提交中…' : '确认取消' }}
        </button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.dialog-backdrop { position: fixed; inset: 0; z-index: 20; display: grid; place-items: center; padding: 1rem; background: #0f172a99; }
.dialog { display: grid; width: min(32rem, 100%); gap: 1rem; padding: 1.25rem; border-radius: 0.75rem; background: #fff; box-shadow: 0 1.5rem 4rem #0f172a55; }
h2, p { margin: 0; }
.dialog__emergency-note { padding: 0.6rem 0.75rem; border: 1px solid #bfdbfe; border-radius: 0.5rem; background: #eff6ff; color: #1d4ed8; font-size: 0.85rem; }
.dialog__label { display: grid; gap: 0.4rem; color: #334155; font-size: 0.85rem; font-weight: 700; }
textarea { padding: 0.75rem; border: 1px solid #cbd5e1; border-radius: 0.45rem; resize: vertical; font: inherit; }
.dialog__count { justify-self: end; color: #64748b; font-size: 0.75rem; font-weight: 400; }
.dialog__error { color: #b91c1c; font-size: 0.8rem; }
.dialog__actions { display: flex; justify-content: flex-end; gap: 0.75rem; }
button { min-height: 2.5rem; padding: 0.45rem 0.75rem; border: 1px solid #cbd5e1; border-radius: 0.45rem; background: #fff; cursor: pointer; }
button.danger { border-color: #b91c1c; background: #b91c1c; color: #fff; }
button:disabled { cursor: not-allowed; opacity: 0.6; }
</style>
