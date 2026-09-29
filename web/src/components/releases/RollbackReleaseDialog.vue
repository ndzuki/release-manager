<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import AppDialog from '@/components/common/AppDialog.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import { mapRollbackError, rollbackRelease, type RollbackFailure } from '@/connect/rollback-api';

/*
 * Rollback confirmation (REQ-056 AC-056-08, A11).
 *
 * The dialog mirrors the server's validation rather than discovering it with a
 * request: a rollback must carry a reason and a target revision that is >= 1 and
 * strictly lower than the release's current revision. On success the owner is told
 * which operation was created so it can navigate to it — the rollback is queued as a
 * NEW operation, never an in-place mutation.
 */
const props = defineProps<{
  open: boolean;
  releaseDefinitionId: string;
  releaseName: string;
  currentRevision: number;
}>();

const emit = defineEmits<{
  close: [];
  created: [result: { operationId: string; fromRevision: number; toRevision: number }];
}>();

const targetRevision = ref('');
const reason = ref('');
const submitting = ref(false);
const failure = ref<RollbackFailure | null>(null);
const validationError = ref('');
/*
 * One idempotency key per ATTEMPT, not per request: the server replays the same
 * operation for a repeated key, which is exactly what a retry after a lost response
 * needs. The key rotates only when the payload changes, so pressing 重试 with the
 * same inputs can never queue a second rollback.
 */
const idempotencyKey = ref('');
let lastPayload = '';

const parsedTarget = computed(() => {
  const value = Number(targetRevision.value);
  return Number.isInteger(value) ? value : Number.NaN;
});

// A fresh dialog must never carry the previous attempt's state.
watch(
  () => props.open,
  (open) => {
    if (!open) return;
    targetRevision.value = '';
    reason.value = '';
    failure.value = null;
    validationError.value = '';
    idempotencyKey.value = crypto.randomUUID();
    lastPayload = '';
  },
  { immediate: true },
);

const canSubmit = computed(() => !submitting.value && props.currentRevision > 1);

function validate(): boolean {
  if (!reason.value.trim()) {
    validationError.value = '回滚必须填写原因';
    return false;
  }
  if (!Number.isInteger(parsedTarget.value) || parsedTarget.value < 1) {
    validationError.value = '目标 Revision 必须是 1 以上的整数';
    return false;
  }
  if (parsedTarget.value >= props.currentRevision) {
    validationError.value = `目标 Revision 必须小于当前 Revision（${props.currentRevision}）`;
    return false;
  }
  validationError.value = '';
  return true;
}

/** Keeps the key while the request is unchanged, rotates it when it changes. */
function keyFor(payload: string): string {
  if (payload !== lastPayload || !idempotencyKey.value) {
    idempotencyKey.value = crypto.randomUUID();
    lastPayload = payload;
  }
  return idempotencyKey.value;
}

async function submit(): Promise<void> {
  if (!validate() || submitting.value) return;
  submitting.value = true;
  failure.value = null;
  try {
    const payload = `${parsedTarget.value}|${reason.value.trim()}`;
    const result = await rollbackRelease({
      releaseDefinitionId: props.releaseDefinitionId,
      targetRevision: parsedTarget.value,
      expectedCurrentRevision: props.currentRevision,
      reason: reason.value.trim(),
      idempotencyKey: keyFor(payload),
    });
    emit('created', {
      operationId: result.operationId,
      fromRevision: result.fromRevision,
      toRevision: result.toRevision,
    });
  } catch (error) {
    failure.value = mapRollbackError(error);
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <AppDialog
    :open="open"
    :title="`回滚 ${releaseName}`"
    :description="`当前 Revision ${currentRevision}，回滚会创建一个新的 ROLLBACK 操作。`"
    danger
    :close-on-backdrop="false"
    :close-on-escape="!submitting"
    @close="emit('close')"
  >
    <p class="rollback-dialog__warning" role="note">
      回滚会按目标 Revision 重新下发该 Release；操作不可撤销，请确认目标 Revision 与原因。
    </p>

    <ErrorState
      v-if="failure"
      title="回滚未成功"
      :message="failure.message"
      :action-label="failure.retryable ? '重试' : ''"
      @action="submit"
    />

    <form class="rollback-dialog__form" @submit.prevent="submit">
      <label>
        当前 Revision（来自发布清单）
        <input :value="currentRevision" name="currentRevision" readonly />
      </label>
      <label>
        目标 Revision
        <input v-model="targetRevision" name="targetRevision" inputmode="numeric" placeholder="例如 1" />
      </label>
      <label>
        原因（必填）
        <textarea v-model="reason" name="reason" rows="3" placeholder="为什么要回滚"></textarea>
      </label>

      <p v-if="validationError" class="rollback-dialog__error" role="alert" data-testid="rollback-validation">
        {{ validationError }}
      </p>

      <div class="rollback-dialog__actions">
        <button type="button" :disabled="submitting" @click="emit('close')">取消</button>
        <button type="submit" class="danger" :disabled="!canSubmit" data-testid="rollback-submit">
          {{ submitting ? '提交中…' : '确认回滚' }}
        </button>
      </div>
    </form>
  </AppDialog>
</template>

<style scoped>
.rollback-dialog__warning {
  margin: 0 0 var(--space-3);
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-warning-border);
  border-radius: var(--radius-md);
  background: var(--color-warning-surface);
  color: var(--color-warning-ink-strong);
  font-size: var(--font-size-sm);
}

.rollback-dialog__form {
  display: grid;
  gap: var(--space-3);
}

.rollback-dialog__form label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.rollback-dialog__form input,
.rollback-dialog__form textarea {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.rollback-dialog__error {
  margin: 0;
  color: var(--color-error);
  font-size: var(--font-size-sm);
}

.rollback-dialog__actions {
  display: flex;
  gap: var(--space-3);
  align-items: center;
  justify-content: flex-end;
}

.rollback-dialog__actions button {
  padding: var(--space-2) var(--space-4);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.rollback-dialog__actions button.danger {
  border-color: var(--color-danger-border);
  background: var(--color-danger-surface);
  color: var(--color-error);
}
</style>
