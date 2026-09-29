<script setup lang="ts">
import { computed, ref } from 'vue';
import { REASON_MAX_CHARS, REASON_MIN_CHARS } from '@/stores/operationTimeline';
import AppDialog from '@/components/common/AppDialog.vue';

const props = defineProps<{
  submitting?: boolean;
  error?: { code: string; message: string } | null;
  emergencyQueued?: boolean;
}>();

const emit = defineEmits<{ submit: [reason: string]; close: [] }>();

const reason = ref('');
const touched = ref(false);

const charCount = computed(() => [...reason.value.trim()].length);
const lengthError = computed(() => {
  if (charCount.value < REASON_MIN_CHARS) return '取消原因不能为空';
  if (charCount.value > REASON_MAX_CHARS) return `取消原因过长（上限 ${REASON_MAX_CHARS} 字符）`;
  return '';
});
const validationError = computed(() => (touched.value ? lengthError.value : ''));

function submit(): void {
  touched.value = true;
  if (lengthError.value || props.submitting) return;
  emit('submit', reason.value.trim());
}
</script>

<template>
  <!-- Focus, Tab containment and Escape come from AppDialog now: the local
       onMounted focus and @keydown.esc this file used to carry are gone. -->
  <AppDialog
    :open="true"
    title="取消发布操作"
    :close-on-escape="!submitting"
    @close="emit('close')"
  >
    <p v-if="emergencyQueued" class="cancel-dialog__emergency-note">
      取消不等于 K8s 回滚，集群中的紧急变更可能仍会生效。
    </p>

    <label class="cancel-dialog__label">
      取消原因（必填）
      <textarea
        v-model="reason"
        rows="5"
        :maxlength="REASON_MAX_CHARS + 20"
        :aria-invalid="Boolean(validationError)"
        aria-describedby="cancel-reason-help cancel-reason-error"
        placeholder="说明取消原因"
      />
      <span id="cancel-reason-help" class="cancel-dialog__count">{{ charCount }}/{{ REASON_MAX_CHARS }}</span>
    </label>

    <p v-if="validationError" id="cancel-reason-error" class="cancel-dialog__error" role="alert">{{ validationError }}</p>
    <p v-else-if="error" class="cancel-dialog__error" role="alert">
      {{ error.message }}<template v-if="error.code">（{{ error.code }}）</template>
    </p>

    <template #footer>
      <button type="button" :disabled="submitting" @click="emit('close')">返回</button>
      <button type="button" class="danger" :disabled="submitting" @click="submit">
        {{ submitting ? '提交中…' : '确认取消' }}
      </button>
    </template>
  </AppDialog>
</template>

<style scoped>
.cancel-dialog__emergency-note {
  margin: 0;
  /* 0.6rem has no step in the 4px spacing scale yet; kept literal to preserve the
     rendered value exactly (scale gap recorded for the design pass). */
  padding: 0.6rem var(--space-3);
  border: 1px solid var(--color-info-border-soft);
  border-radius: var(--radius-lg);
  background: var(--color-info-soft);
  color: var(--color-info);
  font-size: var(--font-size-sm);
}

.cancel-dialog__label {
  display: grid;
  gap: var(--space-1);
  color: var(--color-text-secondary);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.cancel-dialog__label textarea {
  padding: var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  resize: vertical;
  font: inherit;
}

.cancel-dialog__count {
  justify-self: end;
  color: var(--color-muted);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-regular);
}

.cancel-dialog__error {
  margin: 0;
  color: var(--color-error);
  font-size: var(--font-size-sm);
}

.danger {
  border-color: var(--color-error);
  background: var(--color-error);
  color: var(--color-on-accent);
}
</style>
