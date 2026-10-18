import { describe, expect, it, vi } from 'vitest';
import type { OrganizationMemberView, OrganizationRole } from '@/connect/organization-api';
import { useMemberAdd } from './useMemberAdd';

/*
 * TASK-363: the add-member form's state, pinned at the composable seam. AddMember is
 * not convergent, so the local duplicate check (after trimming) is what keeps a
 * repeated submit from being paid for by the server.
 */

function member(overrides: Partial<OrganizationMemberView> = {}): OrganizationMemberView {
  return {
    userId: 'viewer-1',
    role: 'viewer',
    optimisticVersion: 1n,
    createdAt: null,
    updatedAt: null,
    ...overrides,
  };
}

function setup(overrides: {
  members?: OrganizationMemberView[];
  add?: (userId: string, role: OrganizationRole) => Promise<boolean>;
} = {}) {
  const add = vi.fn(overrides.add ?? (async () => true));
  const state = useMemberAdd({
    members: () => overrides.members ?? [member()],
    add,
  });
  return { state, add };
}

describe('useMemberAdd', () => {
  it('starts on the viewer role', () => {
    const { state } = setup();

    expect(state.role.value).toBe('viewer');
    expect(state.userId.value).toBe('');
    expect(state.error.value).toBe('');
  });

  it('refuses a blank user id without spending a request', async () => {
    const { state, add } = setup();
    state.userId.value = '   ';

    const ok = await state.submit();

    expect(ok).toBe(false);
    expect(add).not.toHaveBeenCalled();
    expect(state.error.value).toBe('请填写用户 ID');
  });

  it('refuses a duplicate whose id carries surrounding whitespace', async () => {
    const { state, add } = setup();
    state.userId.value = '  viewer-1  ';

    const ok = await state.submit();

    expect(ok).toBe(false);
    expect(add).not.toHaveBeenCalled();
    expect(state.error.value).toBe('该用户已是组织成员；请直接修改其角色');
  });

  it('trims the id, sends the role and clears only the id on success', async () => {
    const { state, add } = setup();
    state.userId.value = '  new-user  ';
    state.role.value = 'deployer';

    const ok = await state.submit();

    expect(ok).toBe(true);
    expect(add).toHaveBeenCalledWith('new-user', 'deployer');
    expect(state.userId.value).toBe('');
    expect(state.role.value).toBe('deployer');
    expect(state.error.value).toBe('');
  });

  it('keeps the typed id when the server refuses the add', async () => {
    const { state } = setup({ add: async () => false });
    state.userId.value = 'new-user';

    const ok = await state.submit();

    expect(ok).toBe(false);
    expect(state.userId.value).toBe('new-user');
    expect(state.error.value).toBe('');
  });

  it('clears a previous local error when a later submit succeeds', async () => {
    const { state } = setup();
    await state.submit();
    expect(state.error.value).toBe('请填写用户 ID');

    state.userId.value = 'new-user';
    await state.submit();

    expect(state.error.value).toBe('');
  });
});
