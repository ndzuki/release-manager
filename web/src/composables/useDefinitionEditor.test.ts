import { describe, expect, it, vi } from 'vitest';
import type { DefinitionView, UpdateDefinitionInput } from '@/connect/definition-api';
import { useDefinitionEditor } from './useDefinitionEditor';

/*
 * TASK-363: the editor's draft/validation/versioned-write contract, pinned at the
 * composable seam the page now drives. The dialogs render the draft; these cases
 * cover the boundary conditions a DOM test cannot reach cheaply.
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
    promotionMappings: [
      { workloadKind: 'Deployment', workloadName: 'api', container: 'app', field: 'image', valuesPath: 'image.tag' },
    ],
    promotionMappingsViolation: null,
    approvedAnnotationKeys: [],
    approvedAnnotationKeysViolation: null,
    ...overrides,
  };
}

function setup(overrides: { update?: (input: UpdateDefinitionInput) => Promise<boolean>; fresh?: DefinitionView } = {}) {
  const update = vi.fn(overrides.update ?? (async () => true));
  const freshDefinition = vi.fn(() => overrides.fresh);
  const editor = useDefinitionEditor({ update, freshDefinition });
  return { editor, update, freshDefinition };
}

describe('useDefinitionEditor', () => {
  it('starts closed and opens with a copy of the row it was given', () => {
    const row = definition();
    const { editor } = setup();

    expect(editor.editing.value).toBeNull();
    expect(editor.title.value).toBe('');

    editor.startEdit(row);

    expect(editor.editing.value).toEqual(row);
    expect(editor.editing.value?.version).toBe(3n);
    expect(editor.title.value).toBe('编辑 target（版本 3）');
    expect(editor.draftNamespace.value).toBe('ns');
    expect(editor.draftReleaseName.value).toBe('release-a');
    expect(editor.draftChartName.value).toBe('chart');
    expect(editor.draftMappings.value).toEqual(row.promotionMappings);
    // The draft is a copy: editing it must not mutate the row the list still holds.
    expect(editor.draftMappings.value[0]).not.toBe(row.promotionMappings[0]);
  });

  it('refuses to edit a definition whose mapping payload is a contract violation', () => {
    const { editor } = setup();

    editor.startEdit(definition({ promotionMappingsViolation: '无法解析' }));

    expect(editor.editing.value).toBeNull();
  });

  it('adds blank rows and removes the row at the requested index', () => {
    const { editor } = setup();
    editor.startEdit(definition({ promotionMappings: [] }));

    editor.addMapping();
    editor.addMapping();
    expect(editor.draftMappings.value).toHaveLength(2);
    expect(editor.draftMappings.value[0]).toEqual({
      workloadKind: '',
      workloadName: '',
      container: '',
      field: '',
      valuesPath: '',
    });

    editor.removeMapping(0);
    expect(editor.draftMappings.value).toHaveLength(1);
  });

  it('refuses a row missing a required field and names its 1-based position', async () => {
    const { editor, update } = setup();
    editor.startEdit(definition({ promotionMappings: [] }));
    editor.addMapping();
    editor.draftMappings.value[0]!.valuesPath = 'image.tag';

    const ok = await editor.submit();

    expect(ok).toBe(false);
    expect(update).not.toHaveBeenCalled();
    expect(editor.validationError.value).toBe(
      '第 1 行映射缺少必填项（workload kind/name、field、values path）',
    );
    expect(editor.editing.value).not.toBeNull();
  });

  it('submits the trimmed draft with the version it read and closes on success', async () => {
    const { editor, update } = setup();
    editor.startEdit(definition());
    editor.draftNamespace.value = '  ns2  ';
    editor.draftReleaseName.value = '  release-b  ';
    editor.draftChartName.value = '  chart2  ';
    editor.draftMappings.value[0]!.workloadKind = '  StatefulSet  ';

    const ok = await editor.submit();

    expect(ok).toBe(true);
    expect(update).toHaveBeenCalledWith({
      definitionId: 'def-1',
      expectedVersion: 3n,
      namespace: 'ns2',
      releaseName: 'release-b',
      chartName: 'chart2',
      promotionMappings: [
        { workloadKind: 'StatefulSet', workloadName: 'api', container: 'app', field: 'image', valuesPath: 'image.tag' },
      ],
    });
    expect(editor.editing.value).toBeNull();
  });

  it('submits an empty mapping list when every row was removed', async () => {
    const { editor, update } = setup();
    editor.startEdit(definition());
    editor.removeMapping(0);

    await editor.submit();

    expect(update).toHaveBeenCalledWith(expect.objectContaining({ promotionMappings: [] }));
  });

  it('adopts the version the server now holds after a refusal, without touching the draft', async () => {
    const fresh = definition({ version: 4n });
    const { editor } = setup({ update: async () => false, fresh });
    editor.startEdit(definition());
    editor.draftMappings.value[0]!.field = 'image.digest';

    const ok = await editor.submit();

    expect(ok).toBe(false);
    expect(editor.editing.value?.version).toBe(4n);
    expect(editor.editing.value).toEqual(fresh);
    expect(editor.title.value).toBe('编辑 target（版本 4）');
    // The operator's edits must survive: only the version moved.
    expect(editor.draftMappings.value[0]!.field).toBe('image.digest');
  });

  it('keeps the stale object when the refusal did not change the version', async () => {
    const row = definition();
    // Same version, different object: adopting it would be a no-op the page must not
    // claim, and the assertion below proves the old object (name) is still the one on
    // screen.
    const { editor } = setup({ update: async () => false, fresh: definition({ version: 3n, name: 'other' }) });
    editor.startEdit(row);

    await editor.submit();

    expect(editor.editing.value?.version).toBe(3n);
    expect(editor.editing.value?.name).toBe('target');
  });

  it('clears the draft when the owner closes the dialog', () => {
    const { editor } = setup();
    editor.startEdit(definition());

    editor.close();

    expect(editor.editing.value).toBeNull();
  });
});
