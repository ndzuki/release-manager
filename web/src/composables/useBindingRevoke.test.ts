import { describe, expect, it, vi } from 'vitest';
import type { BindingView } from '@/connect/binding-api';
import { useBindingRevoke } from './useBindingRevoke';

/*
 * TASK-363: the destructive revoke confirmation, pinned at the composable seam.
 * The properties that matter are "the RPC runs only after confirming", "the
 * reason lives in the revoke's own slot" and "arming/closing drop the previous
 * attempt's reason" (TASK-303).
 */

function binding(overrides: Partial<BindingView> = {}): BindingView {
  return {
    id: 'b1',
    orgId: 'org-1',
    customerId: 'customer-a',
    status: 'active',
    optimisticVersion: 2n,
    createdAt: null,
    updatedAt: null,
    ...overrides,
  };
}

function setup(overrides: { revoke?: (binding: BindingView) => Promise<boolean>; failure?: string | null } = {}) {
  const revoke = vi.fn(overrides.revoke ?? (async () => true));
  const failureMessage = vi.fn(() => overrides.failure ?? null);
  const state = useBindingRevoke({ revoke, failureMessage });
  return { state, revoke, failureMessage };
}

describe('useBindingRevoke', () => {
  it('arms the confirmation without running the RPC', () => {
    const { state, revoke } = setup();

    state.arm(binding());

    expect(revoke).not.toHaveBeenCalled();
    expect(state.pending.value?.id).toBe('b1');
    expect(state.description.value).toBe(
      '确认后将立即撤销客户 customer-a 的绑定，并收回该绑定授予的能力，此操作不可恢复。',
    );
  });

  it('does nothing when confirmed without an armed binding', async () => {
    const { state, revoke } = setup();

    expect(await state.confirm()).toBe(false);
    expect(revoke).not.toHaveBeenCalled();
    expect(state.failure.value).toBeNull();
  });

  it('revokes the armed binding and closes on success', async () => {
    const { state, revoke } = setup();
    const row = binding();
    state.arm(row);

    expect(await state.confirm()).toBe(true);
    expect(revoke).toHaveBeenCalledWith(row);
    expect(state.pending.value).toBeNull();
    expect(state.failure.value).toBeNull();
  });

  it('keeps the dialog armed and reports the reason on failure', async () => {
    const { state } = setup({ revoke: async () => false, failure: '操作失败，请稍后重试' });
    state.arm(binding());

    expect(await state.confirm()).toBe(false);
    expect(state.pending.value?.id).toBe('b1');
    expect(state.failure.value).toBe('操作失败，请稍后重试');
  });

  // TASK-303: the slot belongs to ONE attempt. Re-arming after a failure must not
  // carry the previous reason into the next confirmation.
  it('drops the previous reason when the confirmation is armed again', () => {
    const { state } = setup();
    state.arm(binding());
    state.failure.value = 'previous failure';

    state.arm(binding({ customerId: 'customer-b' }));

    expect(state.failure.value).toBeNull();
    expect(state.pending.value?.customerId).toBe('customer-b');
  });

  it('clears the reason and the armed binding on close', () => {
    const { state } = setup();
    state.arm(binding());
    state.failure.value = 'previous failure';

    state.close();

    expect(state.pending.value).toBeNull();
    expect(state.failure.value).toBeNull();
    expect(state.description.value).toBe('');
  });
});
