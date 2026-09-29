<script setup lang="ts">
import { computed, shallowRef, useId } from 'vue';
import AppDialog from '@/components/common/AppDialog.vue';

const props = defineProps<{ submitting?: boolean }>();
const emit = defineEmits<{ submit: [reason: string]; close: [] }>();
const reason = shallowRef('');
const error = computed(() => reason.value.length > 1000 ? '拒绝原因过长 (上限 1000 字符)' : '');
const errorId = useId();

function submit(): void {
  if (error.value || props.submitting) return;
  emit('submit', reason.value.trim());
}
</script>

<template>
  <!-- Backdrop click closes, as it did before the migration (HEAD had
       @click.self on the backdrop); only a submission blocks Escape. -->
  <AppDialog
    :open="true"
    title="拒绝 ValuesRevision"
    :close-on-escape="!submitting"
    @close="emit('close')"
  >
    <label class="reject-reason">
      拒绝原因（可选）
      <textarea
        v-model="reason"
        rows="6"
        maxlength="1001"
        placeholder="说明需要修改的内容"
        :aria-invalid="Boolean(error)"
        :aria-describedby="error ? errorId : undefined"
      />
    </label>
    <p v-if="error" :id="errorId" class="reject-reason__error" role="alert">{{ error }}</p>

    <template #footer>
      <button type="button" :disabled="submitting" @click="emit('close')">取消</button>
      <button type="button" class="danger" :disabled="Boolean(error) || submitting" @click="submit">
        {{ submitting ? '提交中…' : '确认拒绝' }}
      </button>
    </template>
  </AppDialog>
</template>

<style scoped>
.reject-reason {
  display: grid;
  gap: var(--space-1);
  color: var(--color-text-secondary);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.reject-reason textarea {
  padding: var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  resize: vertical;
  font: inherit;
}

.reject-reason textarea[aria-invalid='true'] {
  border-color: var(--color-error);
}

.reject-reason__error {
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
