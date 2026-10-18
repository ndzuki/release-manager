<script setup lang="ts">
import { computed, onMounted } from 'vue';
import { t } from '@/i18n/messages';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import Toast from '@/components/common/Toast.vue';
import OrganizationAddMemberForm from '@/components/organization/OrganizationAddMemberForm.vue';
import OrganizationCreateDialog from '@/components/organization/OrganizationCreateDialog.vue';
import OrganizationDisableDialog from '@/components/organization/OrganizationDisableDialog.vue';
import OrganizationMemberTable from '@/components/organization/OrganizationMemberTable.vue';
import { useMemberAdd } from '@/composables/useMemberAdd';
import { useOrganizationCreate } from '@/composables/useOrganizationCreate';
import { useOrganizationDisable } from '@/composables/useOrganizationDisable';
import type { OrganizationMemberView, OrganizationRole } from '@/connect/organization-api';
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
 *
 * TASK-363: the page is a composition surface. The list, the add form and the two
 * dialogs live in components/organization, the create/disable/add state lives in
 * composables, and what stays here is the store wiring, the role projection and the
 * page-level states.
 */
const auth = useAuthStore();
const store = useOrganizationStore();
const orgStore = useOrganizationsStore();

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

const {
  creating: creatingOrg,
  name: newOrgName,
  validationError: createOrgValidation,
  failure: createOrgFailure,
  open: startCreateOrg,
  close: closeCreateOrg,
  submit: submitCreateOrg,
} = useOrganizationCreate({
  create: (name) => orgStore.create(name),
  failureMessage: () => orgStore.failure?.message ?? null,
});

const {
  pending: pendingDisableOrg,
  failure: disableOrgFailure,
  armFailure: orgArmFailure,
  loading: loadingOrg,
  description: disableOrgDescription,
  arm: armDisableOrg,
  close: closeDisableOrg,
  confirm: confirmDisableOrg,
} = useOrganizationDisable({
  organizationId: () => activeOrgId.value,
  read: (orgId) => orgStore.read(orgId),
  disable: (orgId, expectedVersion) => orgStore.disable(orgId, expectedVersion),
  failureMessage: () => orgStore.failure?.message ?? null,
  failureCode: () => orgStore.failure?.code ?? null,
});

const {
  userId: newUserId,
  role: newRole,
  error: addError,
  submit: submitAdd,
} = useMemberAdd({
  members: () => store.members,
  add: (userId, role) => store.add(userId, role),
});

onMounted(() => {
  if (activeOrgId.value) void store.load(activeOrgId.value);
});

/**
 * A rejected change (last platform_admin, missing permission) must not leave the
 * <select> showing the role the server refused: the model value is unchanged, so
 * Vue would not reset the element by itself. The native event comes from the table,
 * which reports which row asked for the change.
 */
async function changeRole(member: OrganizationMemberView, event: Event): Promise<void> {
  const element = event.target as HTMLSelectElement;
  const next = element.value as OrganizationRole;
  if (next === member.role) return;
  const ok = await store.changeRole(member, next);
  if (!ok) element.value = member.role;
}

function removeMember(member: OrganizationMemberView): void {
  void store.remove(member);
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

    <!-- TASK-267: membership write outcomes use the shared Toast primitive. -->
    <Toast
      v-if="store.notice"
      class="organization__notice"
      tone="success"
      testid="organization-notice"
      @dismiss="store.notice = ''"
    >
      {{ store.notice }}
    </Toast>

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

    <OrganizationMemberTable
      v-else-if="store.members.length > 0"
      :members="store.members"
      :can-write="store.canWriteMembership"
      :saving="store.saving"
      :grantable-roles="store.grantableRoles"
      @change-role="changeRole"
      @remove="removeMember"
    />

    <OrganizationAddMemberForm
      v-if="store.canWriteMembership"
      v-model:user-id="newUserId"
      v-model:role="newRole"
      :roles="addableRoles"
      :saving="store.saving"
      :error="addError"
      @submit="submitAdd"
    />

    <!-- TASK-301: create an organization (platform_admin only). The failure is
         rendered inside the dialog, where the user already is. -->
    <OrganizationCreateDialog
      v-model:name="newOrgName"
      :open="creatingOrg"
      :saving="orgStore.saving"
      :validation-error="createOrgValidation"
      :failure="createOrgFailure"
      @close="closeCreateOrg"
      @submit="submitCreateOrg"
    />

    <!-- TASK-301: disabling is terminal, so it confirms through a danger
         alertdialog; the RPC runs from `confirmDisableOrg`, never from the header. -->
    <OrganizationDisableDialog
      v-if="pendingDisableOrg"
      :description="disableOrgDescription"
      :failure="disableOrgFailure"
      :saving="orgStore.saving"
      @close="closeDisableOrg"
      @confirm="confirmDisableOrg"
    />
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

.organization__notice {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-success-border);
  border-radius: var(--radius-lg);
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

.organization__error {
  margin: 0;
  color: var(--color-error);
  font-size: var(--font-size-sm);
}
</style>
