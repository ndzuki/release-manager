import { describe, expect, it, vi } from 'vitest';
import type { OrganizationView } from '@/connect/organization-api';
import { useOrganizationCreate } from './useOrganizationCreate';

/*
 * TASK-363: the create-organization dialog's state, pinned at the composable seam.
 * Create is not idempotent, so the reset-on-open and the required-name check are the
 * two properties that keep a retry from creating a second organization.
 */

function organization(overrides: Partial<OrganizationView> = {}): OrganizationView {
  return {
    id: 'org-2',
    name: 'Acme',
    status: 'active',
    optimisticVersion: 1n,
    createdAt: null,
    updatedAt: null,
    ...overrides,
  };
}

function setup(overrides: {
  create?: (name: string) => Promise<OrganizationView | null>;
  failure?: string | null;
} = {}) {
  const create = vi.fn(overrides.create ?? (async () => organization()));
  const failureMessage = vi.fn(() => overrides.failure ?? null);
  const state = useOrganizationCreate({ create, failureMessage });
  return { state, create, failureMessage };
}

describe('useOrganizationCreate', () => {
  it('resets the name, the validation and the failure when opened', () => {
    const { state } = setup();
    state.name.value = 'leftover';
    state.validationError.value = 'previous';
    state.failure.value = 'previous failure';

    state.open();

    expect(state.creating.value).toBe(true);
    expect(state.name.value).toBe('');
    expect(state.validationError.value).toBe('');
    expect(state.failure.value).toBeNull();
  });

  it('refuses a blank name without spending a request', async () => {
    const { state, create } = setup();
    state.open();
    state.name.value = '   ';

    const ok = await state.submit();

    expect(ok).toBe(false);
    expect(create).not.toHaveBeenCalled();
    expect(state.validationError.value).toBe('组织名称');
    expect(state.creating.value).toBe(true);
  });

  it('trims the name and closes on success', async () => {
    const { state, create } = setup();
    state.name.value = '  Acme  ';

    const ok = await state.submit();

    expect(ok).toBe(true);
    expect(create).toHaveBeenCalledWith('Acme');
    expect(state.creating.value).toBe(false);
  });

  it('keeps the dialog open and reports the mapped reason on refusal', async () => {
    const { state } = setup({
      create: async () => null,
      failure: '无权管理组织：创建组织需要 platform_admin，改名与停用需要 organization/write',
    });
    state.open();
    state.name.value = 'Acme';

    const ok = await state.submit();

    expect(ok).toBe(false);
    expect(state.creating.value).toBe(true);
    expect(state.failure.value).toBe(
      '无权管理组织：创建组织需要 platform_admin，改名与停用需要 organization/write',
    );
  });

  it('closes without touching the draft, matching the page cancel path', () => {
    const { state } = setup();
    state.name.value = 'Acme';

    state.close();

    expect(state.creating.value).toBe(false);
    expect(state.name.value).toBe('Acme');
  });
});
