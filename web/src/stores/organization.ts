import { computed, ref } from 'vue';
import { defineStore } from 'pinia';
import {
  addMember,
  canGrant,
  highestRole,
  listMembers,
  mapMemberError,
  removeMember,
  updateMemberRole,
  type MemberFailure,
  type OrganizationMemberView,
  type OrganizationRole,
} from '@/connect/organization-api';
import { useAuthStore } from '@/stores/auth';

/*
 * Organization membership state (REQ-026 / A1).
 *
 * The store owns the two behaviours the contract makes easy to get wrong:
 *  - UpdateMemberRole is optimistically locked, so an ABORTED answer means "someone
 *    else changed this row": reload and say so instead of leaving the user with a
 *    stale table;
 *  - FAILED_PRECONDITION means the change would leave the organization without a
 *    platform_admin (or that it is disabled) — a stable, non-retryable message.
 */
export const useOrganizationStore = defineStore('organizationMembers', () => {
  const auth = useAuthStore();

  const orgId = ref('');
  const members = ref<OrganizationMemberView[]>([]);
  const loading = ref(false);
  const saving = ref(false);
  const failure = ref<MemberFailure | null>(null);
  const notice = ref('');

  /** Roles the caller may grant in this organization (mirrors store.Role.CanGrant). */
  const grantableRoles = computed<OrganizationRole[]>(() => {
    // auth.roleNames is the auth store's own computed (the app already trusts it
    // for canEnrollOperators); reading it here keeps this scope's reactivity
    // identical to the rest of the console.
    const mine = highestRole(auth.roleNames);
    return (['platform_admin', 'release_admin', 'deployer', 'viewer'] as OrganizationRole[]).filter((role) =>
      canGrant(mine, role),
    );
  });

  const canWriteMembership = computed(() => grantableRoles.value.length > 0);

  /** Fetches members without clearing a failure that is being reported. */
  async function fetchMembers(): Promise<void> {
    loading.value = true;
    try {
      members.value = await listMembers(orgId.value);
    } catch (error) {
      failure.value = mapMemberError(error);
      members.value = [];
    } finally {
      loading.value = false;
    }
  }

  async function load(nextOrgId: string): Promise<void> {
    orgId.value = nextOrgId;
    failure.value = null;
    await fetchMembers();
  }

  function reset(): void {
    orgId.value = '';
    members.value = [];
    failure.value = null;
    notice.value = '';
  }

  /** Runs a membership write, reloading on conflict and surfacing stable failures. */
  async function write(action: () => Promise<void>, successNotice: string): Promise<boolean> {
    saving.value = true;
    failure.value = null;
    notice.value = '';
    try {
      await action();
      await fetchMembers();
      notice.value = successNotice;
      return true;
    } catch (error) {
      const mapped = mapMemberError(error);
      // Refresh the row versions first, then report the conflict: the fetch may set
      // its own failure, and the message we want to show must win.
      if (mapped.code === 'conflict') await fetchMembers();
      failure.value = mapped;
      return false;
    } finally {
      saving.value = false;
    }
  }

  function changeRole(member: OrganizationMemberView, newRole: OrganizationRole): Promise<boolean> {
    return write(
      () => updateMemberRole(orgId.value, member.userId, newRole, member.optimisticVersion),
      `已把 ${member.userId} 的角色改为 ${newRole}`,
    );
  }

  function remove(member: OrganizationMemberView): Promise<boolean> {
    return write(
      () => removeMember(orgId.value, member.userId, member.optimisticVersion),
      `已移除成员 ${member.userId}`,
    );
  }

  function add(userId: string, role: OrganizationRole): Promise<boolean> {
    return write(() => addMember(orgId.value, userId, role), `已添加成员 ${userId}（${role}）`);
  }

  return {
    orgId,
    members,
    loading,
    saving,
    failure,
    notice,
    grantableRoles,
    canWriteMembership,
    load,
    reset,
    changeRole,
    remove,
    add,
  };
});
