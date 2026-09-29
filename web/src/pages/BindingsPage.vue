<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onMounted, ref } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import { AUTHORIZATION_ACTIONS, type AuthorizationAction, type BindingView } from '@/connect/binding-api';
import { useAuthStore } from '@/stores/auth';
import { useGovernanceStore } from '@/stores/governance';

/*
 * Bindings and capability grants (REQ-049 / REQ-027, A2 + A3 of the UX plan).
 *
 * The console could not bind an organization to a customer nor grant the
 * emergency-resolver capability, although both RPCs exist. Note the asymmetry the
 * contract forces: bindings are listable (revoked rows included, for history),
 * while capability grants have NO list RPC — the panel only applies a grant/revoke
 * and reports the source/policy versions the server answered.
 */
const auth = useAuthStore();
const store = useGovernanceStore();

const newCustomerId = ref('');
const createError = ref('');

const subject = ref('');
const action = ref<AuthorizationAction>('release.emergency.resolve');
const desiredState = ref<'grant' | 'revoke'>('grant');
const grantError = ref('');

const activeOrgId = computed(() => auth.activeOrganization?.id ?? '');
const activeOrgName = computed(() => auth.activeOrganization?.name ?? '');
const activeBindings = computed(() => store.bindings.filter((binding) => binding.status === 'active'));

onMounted(() => {
  if (activeOrgId.value) void store.loadBindings(activeOrgId.value);
});

async function submitBinding(): Promise<void> {
  createError.value = '';
  const customerId = newCustomerId.value.trim();
  if (!customerId) {
    createError.value = '请填写客户 ID';
    return;
  }
  // The server refuses an active duplicate with ALREADY_EXISTS; catching it here
  // saves a request and gives the same explanation.
  if (activeBindings.value.some((binding) => binding.customerId === customerId)) {
    createError.value = '该客户已绑定（正在生效）';
    return;
  }
  if (await store.bind(customerId)) newCustomerId.value = '';
}

async function submitGrant(): Promise<void> {
  grantError.value = '';
  const target = subject.value.trim();
  if (!target) {
    grantError.value = '请填写主体（用户 ID）';
    return;
  }
  if (await store.setCapability(target, action.value, desiredState.value === 'revoke')) subject.value = '';
}

function formatTimestamp(value: string | null): string {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : '—';
}

function statusLabel(binding: BindingView): string {
  return binding.status === 'active' ? '生效中' : '已撤销';
}
</script>

<template>
  <section class="governance">
    <header class="governance__header">
      <div>
        <p class="eyebrow">{{ t('governance.eyebrow') }}</p>
        <h1>授权绑定</h1>
        <p class="governance__subtitle">
          {{ activeOrgName || '当前组织' }}
          <code v-if="activeOrgId">{{ activeOrgId }}</code>
        </p>
      </div>
      <button type="button" :disabled="store.loading" @click="store.loadBindings(activeOrgId)">
        {{ store.loading ? '刷新中…' : '刷新' }}
      </button>
    </header>

    <p v-if="store.notice" class="governance__notice" role="status" data-testid="governance-notice">
      {{ store.notice }}
    </p>

    <ErrorState
      v-if="store.failure"
      title="操作未成功"
      :message="store.failure.message"
      :details="store.failure.details"
      :action-label="store.failure.retryable ? '重试' : ''"
      @action="store.loadBindings(activeOrgId)"
    />

    <LoadingState v-if="store.loading && store.bindings.length === 0" message="正在加载绑定…" />
    <EmptyState
      v-else-if="!store.loading && !store.failure && store.bindings.length === 0"
      title="暂无绑定"
      message="该组织还没有与任何客户建立授权绑定。"
    />

    <table v-else-if="store.bindings.length > 0" class="governance__table">
      <thead>
        <tr>
          <th scope="col">客户</th>
          <th scope="col">状态</th>
          <th scope="col">版本</th>
          <th scope="col">建立时间</th>
          <th scope="col">操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="binding in store.bindings" :key="binding.id" :data-testid="`binding-row-${binding.customerId}`">
          <td><code>{{ binding.customerId }}</code></td>
          <td>
            <span class="governance__status" :class="`governance__status--${binding.status}`">{{ statusLabel(binding) }}</span>
          </td>
          <td>{{ binding.optimisticVersion }}</td>
          <td>{{ formatTimestamp(binding.createdAt) }}</td>
          <td>
            <button
              v-if="binding.status === 'active'"
              type="button"
              class="danger"
              :disabled="store.saving"
              @click="store.revoke(binding)"
            >
              撤销
            </button>
            <span v-else class="governance__muted">—</span>
          </td>
        </tr>
      </tbody>
    </table>

    <div class="governance__panels">
      <form class="governance__panel" @submit.prevent="submitBinding">
        <h2>新增绑定</h2>
        <label>
          客户 ID
          <input v-model="newCustomerId" name="customerId" placeholder="客户 ID" />
        </label>
        <button type="submit" :disabled="store.saving">{{ store.saving ? '提交中…' : '绑定' }}</button>
        <p v-if="createError" class="governance__error" role="alert" data-testid="binding-create-error">{{ createError }}</p>
      </form>

      <form class="governance__panel" @submit.prevent="submitGrant">
        <h2>能力授予</h2>
        <p class="governance__hint">
          契约没有「列出既有授权」的 RPC，因此这里只能施加一次授予/撤销并回报服务端返回的版本号。
        </p>
        <label>
          主体（用户 ID）
          <input v-model="subject" name="subject" placeholder="用户 ID" />
        </label>
        <label>
          能力
          <select v-model="action" name="action">
            <option v-for="item in AUTHORIZATION_ACTIONS" :key="item" :value="item">{{ item }}</option>
          </select>
        </label>
        <label>
          动作
          <select v-model="desiredState" name="desiredState">
            <option value="grant">授予</option>
            <option value="revoke">撤销</option>
          </select>
        </label>
        <button type="submit" :disabled="store.saving">{{ store.saving ? '提交中…' : '应用' }}</button>
        <p v-if="store.grantVersions" class="governance__versions" data-testid="grant-versions">
          {{ t('governance.sourcePolicy') }} {{ store.grantVersions.sourceVersion }} / {{ store.grantVersions.policyVersion }}
        </p>
        <p v-if="grantError" class="governance__error" role="alert" data-testid="grant-error">{{ grantError }}</p>
      </form>
    </div>
  </section>
</template>

<style scoped>
.governance {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-5);
}

.governance__header {
  display: flex;
  gap: var(--space-3);
  align-items: flex-start;
  justify-content: space-between;
}

.governance__header h1 {
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

.governance__subtitle {
  margin: var(--space-1) 0 0;
  color: var(--color-muted);
}

.governance__notice {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-success-border);
  border-radius: var(--radius-lg);
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

.governance__table {
  width: 100%;
  border-collapse: collapse;
  background: var(--color-surface);
}

.governance__table th,
.governance__table td {
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border);
  text-align: left;
}

.governance__status {
  padding: 0.1rem var(--space-2);
  border-radius: var(--radius-xl);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
}

.governance__status--active {
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

.governance__status--revoked {
  background: var(--color-surface-muted);
  color: var(--color-muted);
}

.governance__muted {
  color: var(--color-muted);
}

.governance__table button.danger {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-danger-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  color: var(--color-error);
  cursor: pointer;
}

.governance__panels {
  display: grid;
  gap: var(--space-4);
  grid-template-columns: repeat(auto-fit, minmax(20rem, 1fr));
}

.governance__panel {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

.governance__panel h2 {
  margin: 0;
  font-size: var(--font-size-lg);
}

.governance__panel label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.governance__panel input,
.governance__panel select {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.governance__panel button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.governance__hint {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--font-size-xs);
}

.governance__versions {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}

.governance__error {
  margin: 0;
  color: var(--color-error);
  font-size: var(--font-size-sm);
}
</style>
