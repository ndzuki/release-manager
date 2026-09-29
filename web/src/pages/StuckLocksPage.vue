<script setup lang="ts">
import { t } from '@/i18n/messages';
import { onMounted, ref } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import type { StuckLockView } from '@/connect/stuck-lock-api';
import { useAuthStore } from '@/stores/auth';
import { useStuckLocksStore } from '@/stores/stuckLocks';

/*
 * Emergency stuck locks (REQ-087, A10 of the UX plan).
 *
 * A stuck lock is a terminal EMERGENCY whose cluster effect is still UNKNOWN past
 * the observation window — it blocks the definition from accepting new work. The
 * release is irreversible and not replayable, so the dialog forces the operator to
 * choose a mode and justify it, and warns about what each mode records.
 */
const store = useStuckLocksStore();
const auth = useAuthStore();

const target = ref<StuckLockView | null>(null);
const reason = ref('');
const evidence = ref('');
const mode = ref<'' | 'NOT_APPLIED_PROVEN' | 'AUDITED_OVERRIDE'>('');
const acknowledged = ref(false);
const validationError = ref('');

onMounted(() => {
  void store.load('');
});

function open(lock: StuckLockView): void {
  target.value = lock;
  reason.value = '';
  evidence.value = '';
  mode.value = '';
  acknowledged.value = false;
  validationError.value = '';
}

async function submit(): Promise<void> {
  if (!target.value) return;
  // Order matches the server (reason -> evidence -> mode) so the first message the
  // operator sees is the same one a direct API call would produce.
  const trimmed = reason.value.trim();
  if (!trimmed) {
    validationError.value = '必须填写释放原因';
    return;
  }
  // The server counts RUNES, not bytes (1-1000 for the reason, <=500 for evidence).
  if ([...trimmed].length > 1000) {
    validationError.value = '释放原因最多 1000 个字符';
    return;
  }
  if ([...evidence.value].length > 500) {
    validationError.value = '解锁证据最多 500 个字符';
    return;
  }
  if (!mode.value) {
    validationError.value = '必须选择释放模式';
    return;
  }
  if (!acknowledged.value) {
    validationError.value = '请先确认已核实集群效果';
    return;
  }
  validationError.value = '';
  const ok = await store.release({
    intentId: target.value.intentId,
    reason: trimmed,
    mode: mode.value,
    evidence: evidence.value.trim() || undefined,
  });
  if (ok) target.value = null;
}

function formatTimestamp(value: string | null): string {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : '—';
}
</script>

<template>
  <section class="stuck">
    <header class="stuck__header">
      <div>
        <p class="eyebrow">{{ t('stuck.eyebrow') }}</p>
        <h1>卡住的紧急锁</h1>
        <p class="stuck__subtitle">
          终态 EMERGENCY 操作在观察窗口内仍未确认集群效果时会保留锁，定义因此无法接受新任务。
        </p>
      </div>
      <button type="button" :disabled="store.loading" @click="store.load(store.definitionFilter)">
        {{ store.loading ? '刷新中…' : '刷新' }}
      </button>
    </header>

    <form class="stuck__filter" @submit.prevent="store.load(store.definitionFilter)">
      <label>
        Release Definition 过滤（留空 = 当前组织内全部）
        <input v-model="store.definitionFilter" name="releaseDefinitionId" placeholder="Release Definition ID" />
      </label>
      <button type="submit" :disabled="store.loading">查询</button>
    </form>

    <p v-if="store.notice" class="stuck__notice" role="status" data-testid="stuck-notice">{{ store.notice }}</p>

    <ErrorState
      v-if="store.failure"
      title="操作未成功"
      :message="store.failure.message"
      :details="store.failure.details"
      action-label="刷新列表"
      @action="store.load(store.definitionFilter)"
    />

    <LoadingState v-if="store.loading && store.locks.length === 0" message="正在读取卡住的锁…" />
    <EmptyState
      v-else-if="store.isEmpty"
      title="没有卡住的锁"
      message="当前范围内没有「效果未知且已过观察窗口」的紧急操作——这正是期望状态。"
    />

    <table v-else-if="store.locks.length > 0" class="stuck__table">
      <thead>
        <tr>
          <th scope="col">定义 / 操作</th>
          <th scope="col">动作</th>
          <th scope="col">锁定路径</th>
          <th scope="col">终态时间 / 卡住自</th>
          <th scope="col">观察窗口</th>
          <th scope="col">操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="lock in store.locks" :key="lock.intentId" :data-testid="`stuck-lock-${lock.intentId}`">
          <td>
            <code>{{ lock.releaseDefinitionId }}</code>
            <br />
            <small>op {{ lock.operationId }}</small>
          </td>
          <td>{{ lock.action }}</td>
          <td>{{ lock.lockPathSummary }}</td>
          <td>
            {{ formatTimestamp(lock.terminalAt) }}
            <br />
            <small>卡住自 {{ formatTimestamp(lock.stuckSince) }}</small>
          </td>
          <td>{{ lock.observeTimeoutDisplay || '—' }}</td>
          <td>
            <button v-if="auth.canWrite" type="button" class="danger" :disabled="store.releasing" @click="open(lock)">释放锁</button>
          </td>
        </tr>
      </tbody>
    </table>

    <div v-if="target" class="stuck__dialog" role="alertdialog" aria-labelledby="stuck-dialog-title">
      <h2 id="stuck-dialog-title">释放锁 {{ target.lockPathSummary }}</h2>
      <p class="stuck__warning">
        <strong>没有撤销</strong>：释放错误会让一个集群状态仍未知的定义重新接受任务。第二次释放不可重放（会报未找到或状态冲突）。
      </p>
      <form @submit.prevent="submit">
        <fieldset>
          <legend>释放模式</legend>
          <label>
            <input v-model="mode" type="radio" name="mode" value="NOT_APPLIED_PROVEN" />
            NOT_APPLIED_PROVEN —— 能证明命令从未生效（例如从未 ACK_PERSISTED 且操作会话离线）；效果记为 NOT_APPLIED。
          </label>
          <label>
            <input v-model="mode" type="radio" name="mode" value="AUDITED_OVERRIDE" />
            AUDITED_OVERRIDE —— 无法证明未生效但必须接管目标；效果<strong>保留 UNKNOWN</strong>，释放全程审计，迟到的结果仍可能解析效果。
          </label>
        </fieldset>
        <label>
          释放原因（必填，≤1000 字）
          <textarea v-model="reason" name="reason" rows="3" placeholder="为什么可以释放这个锁"></textarea>
        </label>
        <label>
          解锁证据（可选，≤500 字）
          <textarea v-model="evidence" name="evidence" rows="2" placeholder="例如 kubectl get deploy -o yaml 的观察结论"></textarea>
        </label>

        <label class="stuck__ack">
          <input v-model="acknowledged" type="checkbox" name="acknowledged" />
          我已通过集群观察核实该效果确实可以释放（释放后定义会重新接受任务，且没有撤销）
        </label>

        <p v-if="validationError" class="stuck__error" role="alert" data-testid="stuck-validation">{{ validationError }}</p>

        <div class="stuck__dialog-actions">
          <button type="button" :disabled="store.releasing" @click="target = null">取消</button>
          <button type="submit" class="danger" :disabled="store.releasing" data-testid="stuck-submit">
            {{ store.releasing ? '提交中…' : '确认释放' }}
          </button>
        </div>
      </form>
    </div>
  </section>
</template>

<style scoped>
.stuck {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-5);
}

.stuck__header {
  display: flex;
  gap: var(--space-3);
  align-items: flex-start;
  justify-content: space-between;
}

.stuck__header h1 {
  margin: 0;
  font-size: var(--font-size-xl);
}

.eyebrow {
  margin: 0;
  color: var(--color-primary);
  font-size: var(--font-size-xs);
  font-weight: 800;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.stuck__subtitle {
  margin: var(--space-1) 0 0;
  max-width: 46rem;
  color: var(--color-muted);
}

.stuck__filter {
  display: flex;
  gap: var(--space-3);
  align-items: flex-end;
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

.stuck__filter label {
  display: grid;
  flex: 1;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.stuck__filter input {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.stuck__notice {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-success-border);
  border-radius: var(--radius-lg);
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

.stuck__table {
  width: 100%;
  border-collapse: collapse;
  background: var(--color-surface);
}

.stuck__table th,
.stuck__table td {
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border);
  text-align: left;
}

.stuck__table small {
  color: var(--color-muted);
}

.stuck__table button,
.stuck__filter button,
.stuck__dialog-actions button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.stuck__table button.danger,
.stuck__dialog-actions button.danger {
  border-color: var(--color-danger-border);
  color: var(--color-error);
}

.stuck__dialog {
  display: grid;
  gap: var(--space-3);
  max-width: 46rem;
  padding: var(--space-4);
  border: 1px solid var(--color-danger-border);
  border-radius: var(--radius-lg);
  background: var(--color-danger-surface);
}

.stuck__dialog h2 {
  margin: 0;
  font-size: var(--font-size-lg);
}

.stuck__warning {
  margin: 0;
  font-size: var(--font-size-sm);
}

.stuck__dialog form {
  display: grid;
  gap: var(--space-3);
}

.stuck__dialog fieldset {
  display: grid;
  gap: var(--space-2);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
}

.stuck__dialog label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
}

.stuck__dialog textarea {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.stuck__ack {
  display: flex;
  gap: var(--space-2);
  align-items: flex-start;
  font-size: var(--font-size-sm);
}

.stuck__error {
  margin: 0;
  color: var(--color-error);
  font-size: var(--font-size-sm);
}

.stuck__dialog-actions {
  display: flex;
  gap: var(--space-3);
  justify-content: flex-end;
}
</style>
