import { ref, type Ref } from 'vue';
import type { OrganizationMemberView, OrganizationRole } from '@/connect/organization-api';

/*
 * TASK-363: the add-member form's state moved out of OrganizationPage.
 *
 * AddMember is NOT convergent (re-adding an existing member fails with
 * ALREADY_EXISTS), so the page refuses a duplicate locally too, before spending a
 * request. A successful add clears the user id but keeps the chosen role: adding
 * several people to the same role is the common case.
 *
 * `members` and `add` are injected so the local validation and the submit path can
 * be tested without the store.
 */

export interface MemberAddOptions {
  /** The current members, used for the local duplicate check. */
  members: () => OrganizationMemberView[];
  /** The store's add; false when the server refused it. */
  add: (userId: string, role: OrganizationRole) => Promise<boolean>;
}

export interface MemberAdd {
  userId: Ref<string>;
  role: Ref<OrganizationRole>;
  error: Ref<string>;
  submit: () => Promise<boolean>;
}

export function useMemberAdd(options: MemberAddOptions): MemberAdd {
  const userId = ref('');
  const role = ref<OrganizationRole>('viewer');
  const error = ref('');

  async function submit(): Promise<boolean> {
    error.value = '';
    const id = userId.value.trim();
    if (!id) {
      error.value = '请填写用户 ID';
      return false;
    }
    if (options.members().some((member) => member.userId === id)) {
      error.value = '该用户已是组织成员；请直接修改其角色';
      return false;
    }
    const ok = await options.add(id, role.value);
    if (ok) userId.value = '';
    return ok;
  }

  return { userId, role, error, submit };
}
