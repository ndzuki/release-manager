<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onMounted, ref, useId } from 'vue';
import AppDialog from '@/components/common/AppDialog.vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import Toast from '@/components/common/Toast.vue';
import { ORGANIZATION_ROLES, type OrganizationRole } from '@/connect/organization-api';
import type { OrganizationMemberView, OrganizationView } from '@/connect/organization-api';
import { useAuthStore } from '@/stores/auth';
import { useOrganizationStore } from '@/stores/organization';
import { useOrganizationsStore } from '@/stores/organizations';

/*
 * Organization membership (REQ-026 / A1 of the UX plan's missing surfaces).
 *
 * The console had no member surface at all: an administrator could not see who
 * belongs to the active organization, change a role, or remove a member, although
 * the four RPCs have existed since REQ-026.
 *
 * Read is open to `member/read`; the write affordances are shown only for roles
 * that can actually grant something (the server still owns the decision).
 *
 * TASK-301 adds the ORGANIZATION-level half of the same domain, which had no console
 * surface at all: this page is the console's only organization settings surface
 * (`/settings/organization`, the single route whose subject is an organization), and
 * it already displays the active organization, so creating one and disabling the
 * active one belong here rather than on a new route.
 *  - create is platform_admin-only: the handler re-checks the role on top of the
 *    policy, so the button follows that role, not `organization/write`;
 *  - disable needs `organization/write` (platform_admin or release_admin) and is
 *    TERMINAL, so it confirms through a danger alertdialog before the RPC runs.
 */
const auth = useAuthStore();
const store = useOrganizationStore();
const orgStore = useOrganizationsStore();

const newUserId = ref('');
const newRole = ref<OrganizationRole>('viewer');
const addError = ref('');

const activeOrgId = computed(() => auth.activeOrganization?.id ?? '');
const activeOrgName = computed(() => auth.activeOrganization?.name ?? '');
const activeOrgStatus = computed(() => auth.activeOrganization?.status ?? '');

const addableRoles = computed(() => store.grantableRoles);

/** Mirrors OrgService.CreateOrganization's handler-side requireRole. */
const canCreateOrganization = computed(() => auth.roleNames.includes('platform_admin'));
/** Mirrors the `organization/write` policy row. */
const canDisableOrganization = computed(() =>
  auth.roleNames.some((role) => role === 'platform_admin' || role === 'release_admin'),
);

const creatingOrg = ref(false);
const newOrgName = ref('');
const createOrgValidation = ref('');
const createOrgFailure = ref<string | null>(null);
const orgIdPrefix = useId();

function createOrgFieldId(field: string): string {
  return `${orgIdPrefix}-org-${field}`;
}

/*
 * Two separate slots on purpose. `disableOrgFailure` is rendered INSIDE the
 * confirmation dialog (the page-level alert sits behind the fixed backdrop), while
 * `orgArmFailure` reports a failure that happened BEFORE the dialog exists — the
 * fresh read that supplies the version, or the absence of an active organization.
 * Sharing one slot is exactly the TASK-303 defect: the dialog would then announce an
 * error from a different operation.
 */
const pendingDisableOrg = ref<OrganizationView | null>(null);
const disableOrgFailure = ref<string | null>(null);
const orgArmFailure = ref<string | null>(null);
const loadingOrg = ref(false);

const disableOrgDescription = computed(() =>
  pendingDisableOrg.value ? t('organization.disable.description', { name: pendingDisableOrg.value.name }) : '',
);

onMounted(() => {
  if (activeOrgId.value) void store.load(activeOrgId.value);
});

function startCreateOrg(): void {
  createOrgValidation.value = '';
  createOrgFailure.value = null;
  newOrgName.value = '';
  creatingOrg.value = true;
}

async function submitCreateOrg(): Promise<void> {
  createOrgValidation.value = '';
  createOrgFailure.value = null;
  const name = newOrgName.value.trim();
  if (!name) {
    createOrgValidation.value = t('organization.create.name');
    return;
  }
  const created = await orgStore.create(name);
  if (created) {
    creatingOrg.value = false;
    return;
  }
  createOrgFailure.value = orgStore.failure?.message ?? null;
}

/**
 * Reads the organization the server holds NOW before arming the confirmation: the
 * version in the session snapshot is from login time, and DisableOrganization is a
 * compare-and-swap, so the stale value would be refused (ABORTED).
 */
async function armDisableOrg(): Promise<void> {
  orgArmFailure.value = null;
  disableOrgFailure.value = null;
  const orgId = activeOrgId.value;
  if (!orgId) {
    orgArmFailure.value = t('organization.disable.unavailable');
    return;
  }
  loadingOrg.value = true;
  try {
    const fresh = await orgStore.read(orgId);
    if (!fresh) {
      orgArmFailure.value = orgStore.failure?.message ?? null;
      return;
    }
    if (fresh.status === 'disabled') {
      orgArmFailure.value = t('organization.disable.alreadyDisabled');
      return;
    }
    pendingDisableOrg.value = fresh;
  } finally {
    loadingOrg.value = false;
  }
}

function closeDisableOrg(): void {
  disableOrgFailure.value = null;
  pendingDisableOrg.value = null;
}

async function confirmDisableOrg(): Promise<void> {
  const target = pendingDisableOrg.value;
  if (!target) return;
  disableOrgFailure.value = null;
  const disabled = await orgStore.disable(target.id, target.optimisticVersion);
  if (disabled) {
    pendingDisableOrg.value = null;
    return;
  }
  disableOrgFailure.value = orgStore.failure?.message ?? null;
  if (orgStore.failure?.code === 'conflict') {
    // A lost race means the row moved: re-read so the hinted retry carries the
    // version the server now holds instead of the one that was just refused.
    const fresh = await orgStore.read(target.id);
    if (fresh) pendingDisableOrg.value = fresh;
    else disableOrgFailure.value = orgStore.failure?.message ?? disableOrgFailure.value;
  }
}

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
      <div class="organization__header-actions">
        <button
          v-if="canCreateOrganization"
          type="button"
          :disabled="orgStore.saving"
          data-testid="organization-create-open"
          @click="startCreateOrg"
        >
          {{ t('organization.create.open') }}
        </button>
        <button
          v-if="canDisableOrganization && activeOrgStatus !== 'disabled'"
          type="button"
          class="danger"
          :disabled="orgStore.saving || loadingOrg"
          data-testid="organization-disable-open"
          @click="armDisableOrg"
        >
          {{ loadingOrg ? t('organization.disable.submitting') : t('organization.disable.open') }}
        </button>
        <button type="button" :disabled="store.loading" @click="store.load(activeOrgId)">
          {{ store.loading ? '刷新中…' : '刷新' }}
        </button>
      </div>
    </header>

    <!-- TASK-301: the organization-level notice uses the shared Toast primitive. -->
    <Toast v-if="orgStore.notice" tone="success" data-testid="organization-org-notice">
      {{ orgStore.notice }}
    </Toast>

    <!--
      TASK-301: a failure that happened BEFORE the confirmation exists (the read that
      supplies the version, or a missing active organization). The dialog is not open
      here, so this copy is not behind any backdrop.
    -->
    <p
      v-if="orgArmFailure"
      class="organization__error"
      role="alert"
      data-testid="organization-disable-arm-error"
    >
      {{ orgArmFailure }}
    </p>

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

    <!-- TASK-301: create an organization (platform_admin only). The failure is
         rendered inside the dialog, where the user already is. -->
    <AppDialog
      :open="creatingOrg"
      :title="t('organization.create.title')"
      :close-on-backdrop="false"
      :close-on-escape="!orgStore.saving"
      @close="creatingOrg = false"
    >
      <div class="organization__create">
        <p class="organization__hint">{{ t('organization.create.hint') }}</p>
        <form @submit.prevent="submitCreateOrg">
          <div class="organization__field">
            <label :for="createOrgFieldId('name')">{{ t('organization.create.name') }}</label>
            <input :id="createOrgFieldId('name')" v-model="newOrgName" name="organizationName" />
          </div>
          <p
            v-if="createOrgValidation"
            class="organization__error"
            role="alert"
            data-testid="organization-create-validation"
          >
            {{ createOrgValidation }}
          </p>
          <p v-if="createOrgFailure" class="organization__error" role="alert" data-testid="organization-create-error">
            {{ createOrgFailure }}
          </p>
          <div class="organization__actions">
            <button type="button" :disabled="orgStore.saving" @click="creatingOrg = false">
              {{ t('action.cancel') }}
            </button>
            <button type="submit" :disabled="orgStore.saving" data-testid="organization-create-submit">
              {{ orgStore.saving ? t('organization.create.submitting') : t('organization.create.submit') }}
            </button>
          </div>
        </form>
      </div>
    </AppDialog>

    <!-- TASK-301: disabling is terminal, so it confirms through a danger
         alertdialog; the RPC runs from `confirmDisableOrg`, never from the header. -->
    <AppDialog
      v-if="pendingDisableOrg"
      :open="true"
      :title="t('organization.disable.title')"
      :description="disableOrgDescription"
      danger
      :close-on-backdrop="false"
      :close-on-escape="!orgStore.saving"
      @close="closeDisableOrg"
    >
      <p v-if="disableOrgFailure" class="organization__error" role="alert" data-testid="organization-disable-error">
        {{ disableOrgFailure }}
      </p>
      <template #footer>
        <button type="button" :disabled="orgStore.saving" @click="closeDisableOrg">
          {{ t('action.cancel') }}
        </button>
        <button
          type="button"
          class="organization__confirm-danger"
          :disabled="orgStore.saving"
          data-testid="organization-disable-confirm"
          @click="confirmDisableOrg"
        >
          {{ orgStore.saving ? t('organization.disable.submitting') : t('organization.disable.confirm') }}
        </button>
      </template>
    </AppDialog>
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

.organization__header-actions {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.organization__header-actions button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.organization__header-actions button.danger {
  border-color: var(--color-danger-border);
  color: var(--color-error);
}

.organization__create {
  display: grid;
  gap: var(--space-3);
}

.organization__create form {
  display: grid;
  gap: var(--space-3);
}

.organization__field {
  display: grid;
  gap: var(--space-1);
}

.organization__field label {
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.organization__create input {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.organization__create .organization__actions {
  justify-content: flex-end;
}

/* AppDialog's footer supplies the baseline; the danger tone is the caller's rule. */
.organization__confirm-danger {
  border-color: var(--color-danger-border);
  color: var(--color-error);
}

.organization__hint {
  margin: 0;
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
