import { describe, expect, it, vi } from 'vitest';
import type { CreateDefinitionInput } from '@/connect/definition-api';
import { useDefinitionCreate } from './useDefinitionCreate';

/*
 * TASK-363: the create dialog's form contract, pinned at the composable seam.
 * The local validation exists because the server derives the definition's name
 * from release_name: an empty one would create a nameless row.
 */

function setup(overrides: {
  create?: (input: CreateDefinitionInput) => Promise<boolean>;
  failure?: string | null;
} = {}) {
  const create = vi.fn(overrides.create ?? (async () => true));
  const failureMessage = vi.fn(() => overrides.failure ?? null);
  const state = useDefinitionCreate({ create, failureMessage });
  return { state, create, failureMessage };
}

describe('useDefinitionCreate', () => {
  it('opens blank, discarding the previous form and its errors', () => {
    const { state } = setup();
    state.open();
    state.form.value.customerId = 'cust-1';
    state.validationError.value = 'stale error';

    state.close();
    state.open();

    expect(state.creating.value).toBe(true);
    expect(state.form.value).toEqual({
      customerId: '',
      clusterId: '',
      namespace: '',
      releaseName: '',
      chartName: '',
      enabled: true,
    });
    expect(state.validationError.value).toBe('');
    expect(state.failure.value).toBeNull();
  });

  it('refuses an empty required field before sending and stays open', async () => {
    const { state, create } = setup();
    state.open();
    state.form.value.customerId = 'cust-1';
    state.form.value.clusterId = '   ';

    const ok = await state.submit();

    expect(ok).toBe(false);
    expect(create).not.toHaveBeenCalled();
    expect(state.validationError.value).toBe('请填写客户 ID、集群 ID 与 Release 名称');
    expect(state.creating.value).toBe(true);
  });

  it('trims the identifiers and keeps the enabled flag it was given', async () => {
    const { state, create } = setup();
    state.open();
    state.form.value.customerId = '  cust-1  ';
    state.form.value.clusterId = '  cluster-1  ';
    state.form.value.namespace = '  ns  ';
    state.form.value.releaseName = '  new-def  ';
    state.form.value.chartName = '  chart  ';
    state.form.value.enabled = false;

    const ok = await state.submit();

    expect(ok).toBe(true);
    expect(create).toHaveBeenCalledWith({
      customerId: 'cust-1',
      clusterId: 'cluster-1',
      namespace: 'ns',
      releaseName: 'new-def',
      chartName: 'chart',
      enabled: false,
    });
    expect(state.creating.value).toBe(false);
  });

  it('keeps the dialog open and surfaces the mapped reason inside it on refusal', async () => {
    const { state } = setup({ create: async () => false, failure: '相同来源与目标的定义已存在' });
    state.open();
    state.form.value.customerId = 'cust-1';
    state.form.value.clusterId = 'cluster-1';
    state.form.value.releaseName = 'new-def';

    const ok = await state.submit();

    expect(ok).toBe(false);
    expect(state.creating.value).toBe(true);
    expect(state.failure.value).toBe('相同来源与目标的定义已存在');
    // The values survived the refusal.
    expect(state.form.value.releaseName).toBe('new-def');
  });

  it('close only closes; the next open resets the form', () => {
    const { state } = setup();
    state.open();

    state.close();

    expect(state.creating.value).toBe(false);
  });
});
