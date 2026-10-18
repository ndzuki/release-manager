import { describe, expect, it, vi } from 'vitest';
import type { OrganizationView } from '@/connect/organization-api';
import { useOrganizationDisable } from './useOrganizationDisable';

/*
 * TASK-363: the destructive disable confirmation, pinned at the composable seam.
 *
 * Three properties matter: the fresh read supplies the version (never the login-time
 * snapshot), the RPC runs only after confirming, and a lost race adopts the version
 * the re-read returned or the hinted retry would carry the refused version forever.
 */

function organization(overrides: Partial<OrganizationView> = {}): OrganizationView {
  return {
    id: 'org-1',
    name: 'Platform',
    status: 'active',
    optimisticVersion: 9n,
    createdAt: null,
    updatedAt: null,
    ...overrides,
  };
}

function setup(overrides: {
  organizationId?: string;
  read?: (orgId: string) => Promise<OrganizationView | null>;
  disable?: (orgId: string, expectedVersion: bigint) => Promise<OrganizationView | null>;
  failure?: string | null;
  failureCode?: string | null;
} = {}) {
  const read = vi.fn(overrides.read ?? (async () => organization()));
  const disable = vi.fn(overrides.disable ?? (async () => organization({ status: 'disabled' })));
  const failureMessage = vi.fn(() => overrides.failure ?? null);
  const failureCode = vi.fn(() => overrides.failureCode ?? null);
  const state = useOrganizationDisable({
    organizationId: () => overrides.organizationId ?? 'org-1',
    read,
    disable,
    failureMessage,
    failureCode,
  });
  return { state, read, disable, failureMessage, failureCode };
}

describe('useOrganizationDisable', () => {
  it('reports a missing active organization without reading', async () => {
    const { state, read } = setup({ organizationId: '' });

    await state.arm();

    expect(read).not.toHaveBeenCalled();
    expect(state.armFailure.value).toBe('当前没有可停用的组织（会话未返回活动组织）。');
    expect(state.pending.value).toBeNull();
  });

  it('reports a failed fresh read on the page, not inside a dialog', async () => {
    const { state } = setup({
      read: async () => null,
      failure: '组织不存在（可能已被删除）',
    });

    await state.arm();

    expect(state.pending.value).toBeNull();
    expect(state.armFailure.value).toBe('组织不存在（可能已被删除）');
  });

  it('refuses an organization the server already reports as disabled', async () => {
    const { state } = setup({ read: async () => organization({ status: 'disabled' }) });

    await state.arm();

    expect(state.pending.value).toBeNull();
    expect(state.armFailure.value).toBe('该组织已停用，无法再次停用。');
  });

  it('arms the confirmation with the freshly read organization', async () => {
    const fresh = organization({ name: 'Fresh', optimisticVersion: 9n });
    const { state, read, disable } = setup({ read: async () => fresh });

    await state.arm();

    expect(read).toHaveBeenCalledWith('org-1');
    expect(disable).not.toHaveBeenCalled();
    expect(state.pending.value).toEqual(fresh);
    expect(state.description.value).toContain('Fresh');
    expect(state.loading.value).toBe(false);
  });

  it('reports the in-flight read while arming', async () => {
    let resolveRead: ((value: OrganizationView) => void) | undefined;
    const { state } = setup({
      read: () =>
        new Promise((resolve) => {
          resolveRead = resolve;
        }),
    });

    const arming = state.arm();
    expect(state.loading.value).toBe(true);

    resolveRead?.(organization());
    await arming;

    expect(state.loading.value).toBe(false);
  });

  it('does nothing when confirmed without an armed organization', async () => {
    const { state, disable } = setup();

    const ok = await state.confirm();

    expect(ok).toBe(false);
    expect(disable).not.toHaveBeenCalled();
  });

  it('disables with the armed version and closes on success', async () => {
    const { state, disable } = setup();
    await state.arm();

    const ok = await state.confirm();

    expect(ok).toBe(true);
    expect(disable).toHaveBeenCalledWith('org-1', 9n);
    expect(state.pending.value).toBeNull();
    expect(state.failure.value).toBeNull();
  });

  it('reports a non-conflict refusal inside the dialog without re-reading', async () => {
    const { state, read } = setup({
      disable: async () => null,
      failure: '组织不存在（可能已被删除）',
      failureCode: 'not_found',
    });
    await state.arm();
    read.mockClear();

    const ok = await state.confirm();

    expect(ok).toBe(false);
    expect(read).not.toHaveBeenCalled();
    expect(state.pending.value).not.toBeNull();
    expect(state.failure.value).toBe('组织不存在（可能已被删除）');
  });

  it('adopts the version the re-read returns after a conflict', async () => {
    const read = vi
      .fn<(orgId: string) => Promise<OrganizationView | null>>()
      .mockResolvedValueOnce(organization({ optimisticVersion: 9n }))
      .mockResolvedValueOnce(organization({ optimisticVersion: 10n }));
    const { state } = setup({
      read,
      disable: async () => null,
      failure: '组织信息已被其他人修改，已为你刷新最新数据，请重试',
      failureCode: 'conflict',
    });
    await state.arm();

    const ok = await state.confirm();

    expect(ok).toBe(false);
    expect(read).toHaveBeenCalledTimes(2);
    expect(state.pending.value?.optimisticVersion).toBe(10n);
    expect(state.failure.value).toBe('组织信息已被其他人修改，已为你刷新最新数据，请重试');
  });

  it('keeps the conflict reason when the re-read itself fails', async () => {
    const read = vi
      .fn<(orgId: string) => Promise<OrganizationView | null>>()
      .mockResolvedValueOnce(organization({ optimisticVersion: 9n }))
      .mockResolvedValueOnce(null);
    const failureMessage = vi
      .fn<() => string | null>()
      .mockReturnValueOnce('组织信息已被其他人修改，已为你刷新最新数据，请重试')
      .mockReturnValueOnce('操作失败，请稍后重试');
    const state = useOrganizationDisable({
      organizationId: () => 'org-1',
      read,
      disable: async () => null,
      failureMessage,
      failureCode: () => 'conflict',
    });
    await state.arm();

    const ok = await state.confirm();

    expect(ok).toBe(false);
    // There is no fresh version to adopt, so the refused row stays armed, and the
    // reader's failure replaces the conflict hint in the dialog.
    expect(state.pending.value?.optimisticVersion).toBe(9n);
    expect(state.failure.value).toBe('操作失败，请稍后重试');
  });

  it('clears the reason and the armed organization on cancel', async () => {
    const { state } = setup();
    await state.arm();
    state.failure.value = 'previous failure';

    state.close();

    expect(state.pending.value).toBeNull();
    expect(state.failure.value).toBeNull();
  });
});
