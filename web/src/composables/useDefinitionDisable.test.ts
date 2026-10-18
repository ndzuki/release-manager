import { describe, expect, it, vi } from 'vitest';
import type { DefinitionView } from '@/connect/definition-api';
import { useDefinitionDisable } from './useDefinitionDisable';

/*
 * TASK-363: the destructive disable confirmation, pinned at the composable seam.
 * The two properties that matter are "the RPC runs only after confirming" and "a
 * lost race adopts the version the server now holds, or the retry fails forever".
 */

function definition(overrides: Partial<DefinitionView> = {}): DefinitionView {
  return {
    id: 'def-1',
    name: 'target',
    customerId: 'cust-1',
    clusterId: 'cluster-1',
    namespace: 'ns',
    releaseName: 'release-a',
    chartName: 'chart',
    status: 'active',
    version: 3n,
    hpaManaged: false,
    maxEmergencyReplicas: 0,
    createdAt: null,
    updatedAt: null,
    promotionMappings: [],
    promotionMappingsViolation: null,
    approvedAnnotationKeys: [],
    approvedAnnotationKeysViolation: null,
    ...overrides,
  };
}

function setup(overrides: {
  disable?: (definition: DefinitionView) => Promise<boolean>;
  fresh?: DefinitionView;
  failure?: string | null;
} = {}) {
  const disable = vi.fn(overrides.disable ?? (async () => true));
  const freshDefinition = vi.fn(() => overrides.fresh);
  const failureMessage = vi.fn(() => overrides.failure ?? null);
  const state = useDefinitionDisable({ disable, freshDefinition, failureMessage });
  return { state, disable, freshDefinition, failureMessage };
}

describe('useDefinitionDisable', () => {
  it('arms the confirmation without running the RPC', () => {
    const { state, disable } = setup();

    state.arm(definition());

    expect(disable).not.toHaveBeenCalled();
    expect(state.pending.value?.id).toBe('def-1');
    expect(state.description.value).toBe(
      '确认后将停用定义 target：不再有新的操作可以指向它，既有历史与清单会保留。',
    );
  });

  it('does nothing when confirmed without an armed definition', async () => {
    const { state, disable } = setup();

    const ok = await state.confirm();

    expect(ok).toBe(false);
    expect(disable).not.toHaveBeenCalled();
  });

  it('disables the armed definition and closes on success', async () => {
    const { state, disable } = setup();
    const row = definition();
    state.arm(row);

    const ok = await state.confirm();

    expect(ok).toBe(true);
    expect(disable).toHaveBeenCalledWith(row);
    expect(state.pending.value).toBeNull();
    expect(state.failure.value).toBeNull();
  });

  it('keeps the dialog open, reports the reason and adopts the refreshed version', async () => {
    const fresh = definition({ version: 4n });
    const { state } = setup({
      disable: async () => false,
      fresh,
      failure: '定义已被其他人修改（版本冲突），已刷新最新数据，请重试',
    });
    state.arm(definition());

    const ok = await state.confirm();

    expect(ok).toBe(false);
    expect(state.pending.value?.version).toBe(4n);
    expect(state.description.value).toBe(
      '确认后将停用定义 target：不再有新的操作可以指向它，既有历史与清单会保留。',
    );
    expect(state.failure.value).toBe('定义已被其他人修改（版本冲突），已刷新最新数据，请重试');
  });

  it('keeps the object it had when the refresh did not change the version', async () => {
    const { state } = setup({ disable: async () => false, fresh: definition({ version: 3n, name: 'other' }) });
    state.arm(definition());

    await state.confirm();

    expect(state.pending.value?.name).toBe('target');
  });

  it('clears the reason and the armed row on cancel', () => {
    const { state } = setup();
    state.arm(definition());
    state.failure.value = 'previous failure';

    state.close();

    expect(state.pending.value).toBeNull();
    expect(state.failure.value).toBeNull();
  });
});
