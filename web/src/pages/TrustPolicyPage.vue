<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
import { useAuthStore } from '@/stores/auth';
import { computed, onMounted, ref } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import { TRUST_ENVIRONMENTS, allowedActions, type TrustAction, type TrustRootView } from '@/connect/trust-api';
import { useTrustPolicyStore } from '@/stores/trustPolicy';

/*
 * Signature trust roots (REQ-012 / REQ-043, A5 of the UX plan).
 *
 * GetTrustPolicy returns the WHOLE snapshot — every root ever created, retired and
 * revoked ones included — plus the policy version and revocation epoch. Writes are
 * state-ordered, so each row offers only the actions the current state accepts and
 * the page re-reads after every refusal.
 */
const store = useTrustPolicyStore();
const auth = useAuthStore();
const pendingAction = ref<{ root: TrustRootView; action: TrustAction } | null>(null);

const environments = TRUST_ENVIRONMENTS;

onMounted(() => {
  void store.load(store.environment);
});

const liveCount = computed(() => store.liveRootCount);

const ACTION_LABELS: Record<TrustAction, string> = {
  end_grace: '结束宽限',
  retire: '退休',
  revoke: '吊销',
};

/** Why an action is unavailable, so the tooltip never states a wrong reason. */
function disabledReason(root: TrustRootView, action: TrustAction): string | undefined {
  if (store.saving) return undefined; // in flight: not a policy reason
  if (!store.canApply(root, action)) return '该环境只剩一个可用信任根，不能移除';
  return undefined;
}

function request(root: TrustRootView, action: TrustAction): void {
  if (!store.canApply(root, action)) return;
  if (action === 'revoke') {
    pendingAction.value = { root, action };
    return;
  }
  void store.apply(root, action);
}

async function confirm(): Promise<void> {
  const pending = pendingAction.value;
  if (!pending) return;
  await store.apply(pending.root, pending.action);
  pendingAction.value = null;
}

function formatTimestamp(value: string | null): string {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : '—';
}

</script>

<template>
  <section class="trust">
    <header class="trust__header">
      <div>
        <p class="eyebrow">{{ t('trust.eyebrow') }}</p>
        <h1>签名信任根</h1>
        <p class="trust__subtitle">
          <!-- The prose word was already there; making it the control's <label> is what
               associates it with the select (the wrapping <p> is a flex container, so the
               label stays a separate flex item and the rendering is unchanged). -->
          <label for="trust-environment">环境</label>
          <select id="trust-environment" v-model="store.environment" name="environment" @change="store.load(store.environment)">
            <option v-for="env in environments" :key="env" :value="env">{{ env }}</option>
          </select>
          <span v-if="store.policy">
            策略版本 <strong>{{ store.policy.version }}</strong> {{ t('trust.revocationEpoch') }} <strong>{{ store.policy.revocationEpoch }}</strong>
            · 可用信任根 <strong>{{ liveCount }}</strong>
          </span>
        </p>
      </div>
      <button type="button" :disabled="store.loading" @click="store.load(store.environment)">
        {{ store.loading ? '刷新中…' : '刷新' }}
      </button>
    </header>

    <p v-if="store.notice" class="trust__notice" role="status" data-testid="trust-notice">{{ store.notice }}</p>

    <ErrorState
      v-if="store.failure"
      title="信任根操作未成功"
      :message="store.failure.message"
      :details="store.failure.details"
      action-label="刷新策略"
      @action="store.load(store.environment)"
    />

    <LoadingState v-if="store.loading && !store.policy" message="正在读取信任策略…" />
    <EmptyState
      v-else-if="store.isEmpty"
      title="该环境还没有信任根"
      message="没有任何签名密钥被信任（策略版本 1、epoch 0）——这是「尚未配置」而不是错误。"
    />

    <table v-else-if="store.policy && store.policy.roots.length > 0" class="trust__table">
      <thead>
        <tr>
          <th scope="col">{{ t('trust.keyId') }}</th>
          <th scope="col">状态</th>
          <th scope="col">{{ t('trust.issuerSubject') }}</th>
          <th scope="col">生效自 / 宽限至</th>
          <th scope="col">操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="root in store.policy.roots" :key="root.id" :data-testid="`trust-root-${root.keyId}`">
          <td><code>{{ root.keyId }}</code></td>
          <td>
            <span class="trust__state" :class="`trust__state--${root.state}`">{{ statusLabel('trustRoot', root.state) }}</span>
          </td>
          <td>
            {{ root.issuer }}
            <br />
            <small>{{ root.subjectPattern }}</small>
          </td>
          <td>
            {{ formatTimestamp(root.validFrom) }}
            <br />
            <small>宽限至 {{ formatTimestamp(root.graceUntil) }}</small>
          </td>
          <td v-if="auth.canManageTrustRoots" class="trust__actions">
            <button
              v-for="action in allowedActions(root.state)"
              :key="action"
              type="button"
              :class="{ danger: action === 'revoke' }"
              :disabled="store.saving || !store.canApply(root, action)"
              :title="disabledReason(root, action)"
              :data-testid="`trust-${action}-${root.keyId}`"
              @click="request(root, action)"
            >
              {{ ACTION_LABELS[action] }}
            </button>
          </td>
        </tr>
      </tbody>
    </table>

    <div v-if="pendingAction" class="trust__confirm" role="alertdialog" aria-labelledby="trust-confirm-title">
      <h2 id="trust-confirm-title">确认吊销信任根 {{ pendingAction.root.keyId }}？</h2>
      <p>
        吊销会立即停止该密钥的签名校验，并<strong>提升吊销 epoch</strong>（缓存持有者据此失效）；记录会保留。
        已用该密钥验证过的制品不会因此失去既有地位。
      </p>
      <div class="trust__confirm-actions">
        <button type="button" :disabled="store.saving" @click="pendingAction = null">取消</button>
        <button type="button" class="danger" :disabled="store.saving" data-testid="trust-confirm-revoke" @click="confirm">
          {{ store.saving ? '提交中…' : '确认吊销' }}
        </button>
      </div>
    </div>
  </section>
</template>

<style scoped>
.trust {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-5);
}

.trust__header {
  display: flex;
  gap: var(--space-3);
  align-items: flex-start;
  justify-content: space-between;
}

.trust__header h1 {
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

.trust__subtitle {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  align-items: center;
  margin: var(--space-1) 0 0;
  color: var(--color-muted);
}

.trust__subtitle select {
  padding: var(--space-1) var(--space-2);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.trust__notice {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-success-border);
  border-radius: var(--radius-lg);
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

.trust__table {
  width: 100%;
  border-collapse: collapse;
  background: var(--color-surface);
}

.trust__table th,
.trust__table td {
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border);
  text-align: left;
}

.trust__table small {
  color: var(--color-muted);
}

.trust__state {
  padding: 0.1rem var(--space-2);
  border-radius: var(--radius-xl);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
}

.trust__state--active {
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

.trust__state--grace {
  background: var(--color-warning-surface);
  color: var(--color-warning-ink-strong);
}

.trust__state--retired,
.trust__state--revoked {
  background: var(--color-surface-muted);
  color: var(--color-muted);
}

.trust__actions {
  display: flex;
  gap: var(--space-2);
}

.trust__actions button,
.trust__confirm-actions button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.trust__actions button.danger,
.trust__confirm-actions button.danger {
  border-color: var(--color-danger-border);
  color: var(--color-error);
}

.trust__actions button:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

.trust__confirm {
  display: grid;
  gap: var(--space-2);
  max-width: 40rem;
  padding: var(--space-4);
  border: 1px solid var(--color-danger-border);
  border-radius: var(--radius-lg);
  background: var(--color-danger-surface);
}

.trust__confirm h2 {
  margin: 0;
  font-size: var(--font-size-lg);
}

.trust__confirm-actions {
  display: flex;
  gap: var(--space-3);
  justify-content: flex-end;
}
</style>
