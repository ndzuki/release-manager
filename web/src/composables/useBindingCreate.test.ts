import { describe, expect, it, vi } from 'vitest';
import type { BindingView } from '@/connect/binding-api';
import { useBindingCreate } from './useBindingCreate';

/*
 * TASK-363: the new-binding form, pinned at the composable seam.
 * The properties that matter are "an active duplicate is refused locally", "a
 * revoked pair is NOT a duplicate" and "the id is trimmed and cleared only on
 * success".
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

function setup(overrides: {
  bind?: (customerId: string) => Promise<boolean>;
  active?: BindingView[];
} = {}) {
  const bind = vi.fn(overrides.bind ?? (async () => true));
  const activeBindings = vi.fn(() => overrides.active ?? []);
  const state = useBindingCreate({ bind, activeBindings });
  return { state, bind, activeBindings };
}

describe('useBindingCreate', () => {
  it('requires a non-blank customer id without spending a request', async () => {
    const { state, bind } = setup();

    expect(await state.submit()).toBe(false);
    expect(state.error.value).toBe('请填写客户 ID');

    state.customerId.value = '   ';
    expect(await state.submit()).toBe(false);
    expect(bind).not.toHaveBeenCalled();
  });

  it('refuses a customer that is already actively bound', async () => {
    const { state, bind } = setup({ active: [binding()] });
    state.customerId.value = 'customer-a';

    expect(await state.submit()).toBe(false);
    expect(state.error.value).toBe('该客户已绑定（正在生效）');
    expect(bind).not.toHaveBeenCalled();
  });

  it('lets a revoked pair through: reactivating it is the normal path', async () => {
    const { state, bind } = setup({ active: [] });
    state.customerId.value = 'customer-b';

    expect(await state.submit()).toBe(true);
    expect(bind).toHaveBeenCalledWith('customer-b');
  });

  it('trims the id and clears it on success', async () => {
    const { state, bind } = setup();
    state.customerId.value = '  customer-c  ';

    expect(await state.submit()).toBe(true);
    expect(bind).toHaveBeenCalledWith('customer-c');
    expect(state.customerId.value).toBe('');
  });

  it('keeps the typed id and stays quiet on a server refusal', async () => {
    const { state } = setup({ bind: async () => false });
    state.customerId.value = 'customer-c';

    expect(await state.submit()).toBe(false);
    expect(state.customerId.value).toBe('customer-c');
    expect(state.error.value).toBe('');
  });

  it('clears the previous validation error on the next submit', async () => {
    const { state } = setup();
    await state.submit();
    expect(state.error.value).not.toBe('');

    state.customerId.value = 'customer-c';
    await state.submit();

    expect(state.error.value).toBe('');
  });
});
