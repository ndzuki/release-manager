<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onMounted, ref } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import { ORGANIZATION_ROLES, type OrganizationRole } from '@/connect/organization-api';
import type { OrganizationMemberView } from '@/connect/organization-api';
import { useAuthStore } from '@/stores/auth';
import { useOrganizationStore } from '@/stores/organization';

/*
 * Organization membership (REQ-026 / A1 of the UX plan's missing surfaces).
 *
 * The console had no member surface at all: an administrator could not see who
 * belongs to the active organization, change a role, or remove a member, although
 * the four RPCs have existed since REQ-026.
 *
 * Read is open to `member/read`; the write affordances are shown only for roles
 * that can actually grant something (the server still owns the decision).
 */
const auth = useAuthStore();
const store = useOrganizationStore();

const newUserId = ref('');
const newRole = ref<OrganizationRole>('viewer');
const addError = ref('');

const activeOrgId = computed(() => auth.activeOrganization?.id ?? '');
const activeOrgName = computed(() => auth.activeOrganization?.name ?? '');

const addableRoles = computed(() => store.grantableRoles);

onMounted(() => {
  if (activeOrgId.value) void store.load(activeOrgId.value);
});

async function submitAdd(): Promise<void> {
  addError.value = '';
  const userId = newUserId.value.trim();
  if (!userId) {
    addError.value = '请填写用户 ID';
    return;
  }
  if (store.members.some((member) => member.userId === userId)) {
    addError.value = '该用户已是组织成员；请直接修改其角色';
    return;
  }
  // AddMember is not convergent: re-adding fails, so refuse duplicates locally too.
  const ok = await store.add(userId, newRole.value);
  if (ok) newUserId.value = '';
}

/**
 * A rejected change (last platform_admin, missing permission) must not leave the
 * <select> showing the role the server refused: the model value is unchanged, so
 * Vue would not reset the element by itself.
 */
async function changeRole(member: OrganizationMemberView, event: Event): Promise<void> {
  const element = event.target as HTMLSelectElement;
  const next = element.value as OrganizationRole;
  if (next === member.role) return;
  const ok = await store.changeRole(member, next);
  if (!ok) element.value = member.role;
}

function formatTimestamp(value: string | null): string {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : '—';
}
</script>

<template>
  <section class="organization">
    <header class="organization__header">
      <div>
        <p class="eyebrow">{{ t('governance.eyebrow') }}</p>
        <h1>组织成员</h1>
        <p class="organization__subtitle">
          {{ activeOrgName || '当前组织' }}
          <code v-if="activeOrgId">{{ activeOrgId }}</code>
        </p>
      </div>
      <button type="button" :disabled="store.loading" @click="store.load(activeOrgId)">
        {{ store.loading ? '刷新中…' : '刷新' }}
      </button>
    </header>

    <p v-if="store.notice" class="organization__notice" role="status" data-testid="organization-notice">
      {{ store.notice }}
    </p>

    <ErrorState
      v-if="store.failure"
      title="成员操作未成功"
      :message="store.failure.message"
      :details="store.failure.details"
      :action-label="store.failure.retryable ? '重试' : ''"
      @action="store.load(activeOrgId)"
    />

    <LoadingState v-if="store.loading && store.members.length === 0" message="正在加载成员…" />
    <EmptyState
      v-else-if="!store.loading && !store.failure && store.members.length === 0"
      title="暂无成员"
      message="该组织还没有成员记录。"
    />

    <table v-else-if="store.members.length > 0" class="organization__table">
      <thead>
        <tr>
          <th scope="col">用户</th>
          <th scope="col">角色</th>
          <th scope="col">版本</th>
          <th scope="col">加入时间</th>
          <th scope="col">操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="member in store.members" :key="member.userId" :data-testid="`member-row-${member.userId}`">
          <td><code>{{ member.userId }}</code></td>
          <td>
            <span class="organization__role" :class="`organization__role--${member.role}`">{{ member.role }}</span>
          </td>
          <td>{{ member.optimisticVersion }}</td>
          <td>{{ formatTimestamp(member.createdAt) }}</td>
          <td v-if="store.canWriteMembership" class="organization__actions">
            <label class="visually-hidden" :for="`role-${member.userId}`">修改 {{ member.userId }} 的角色</label>
            <select
              :id="`role-${member.userId}`"
              :value="member.role"
              :disabled="store.saving"
              @change="changeRole(member, $event)"
            >
              <option v-for="role in ORGANIZATION_ROLES" :key="role" :value="role" :disabled="!store.grantableRoles.includes(role)">
                {{ role }}
              </option>
            </select>
            <button type="button" class="danger" :disabled="store.saving" @click="store.remove(member)">移除</button>
          </td>
          <td v-else class="organization__readonly">只读</td>
        </tr>
      </tbody>
    </table>

    <form v-if="store.canWriteMembership" class="organization__add" @submit.prevent="submitAdd">
      <h2>添加成员</h2>
      <label>
        用户 ID
        <input v-model="newUserId" name="userId" placeholder="用户 ID" />
      </label>
      <label>
        角色
        <select v-model="newRole" name="role">
          <option v-for="role in addableRoles" :key="role" :value="role">{{ role }}</option>
        </select>
      </label>
      <button type="submit" :disabled="store.saving">{{ store.saving ? '提交中…' : '添加' }}</button>
      <p v-if="addError" class="organization__error" role="alert" data-testid="organization-add-error">{{ addError }}</p>
    </form>
  </section>
</template>

<style scoped>
.organization {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-5);
}

.organization__header {
  display: flex;
  gap: var(--space-3);
  align-items: flex-start;
  justify-content: space-between;
}

.organization__header h1 {
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

.organization__subtitle {
  margin: var(--space-1) 0 0;
  color: var(--color-muted);
}

.organization__notice {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-success-border);
  border-radius: var(--radius-lg);
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

.organization__table {
  width: 100%;
  border-collapse: collapse;
  background: var(--color-surface);
}

.organization__table th,
.organization__table td {
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border);
  text-align: left;
}

.organization__role {
  padding: 0.1rem var(--space-2);
  border-radius: var(--radius-xl);
  background: var(--color-surface-muted);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
}

.organization__role--platform_admin {
  background: var(--color-info-subtle);
  color: var(--color-info-ink-strong);
}

.organization__actions {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.organization__actions button,
.organization__add button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.organization__actions button.danger {
  border-color: var(--color-danger-border);
  color: var(--color-error);
}

.organization__readonly {
  color: var(--color-muted);
}

.organization__add {
  display: flex;
  gap: var(--space-3);
  align-items: flex-end;
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

.organization__add h2 {
  margin: 0;
  font-size: var(--font-size-lg);
}

.organization__add label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.organization__add input,
.organization__add select {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.organization__error {
  margin: 0;
  color: var(--color-error);
  font-size: var(--font-size-sm);
}
</style>
