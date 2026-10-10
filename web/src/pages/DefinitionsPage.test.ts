import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';
import { createMemoryHistory, createRouter } from 'vue-router';
import DefinitionsPage from './DefinitionsPage.vue';
import * as api from '@/connect/definition-api';

vi.mock('@/connect/definition-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, listDefinitions: vi.fn(), updateDefinition: vi.fn() };
});

const mounted: Array<{ unmount: () => void }> = [];

/*
 * TASK-281: the editor is an AppDialog now, and AppDialog teleports to document.body,
 * so Vue Test Utils' wrapper scoping no longer reaches the editor. Page-level nodes
 * stay on the wrapper; dialog content is queried in the document (the pattern the
 * other page tests use for teleported overlays).
 */
afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
});

function editorRoot(): HTMLElement {
  const root = document.querySelector<HTMLElement>('.definitions__editor');
  if (!root) throw new Error('definitions editor dialog not rendered');
  return root;
}

function editorDialogPanel(): HTMLElement {
  const panel = document.querySelector<HTMLElement>('.app-dialog__panel');
  if (!panel) throw new Error('definitions editor dialog not rendered');
  return panel;
}

async function clickTestId(id: string): Promise<void> {
  const element = document.querySelector<HTMLElement>(`[data-testid="${id}"]`);
  if (!element) throw new Error(`missing data-testid=${id}`);
  element.click();
  await nextTick();
}

function setEditorInput(name: string, value: string): void {
  const input = document.querySelector<HTMLInputElement>(`.definitions__editor input[name="${name}"]`);
  if (!input) throw new Error(`missing editor input ${name}`);
  input.value = value;
  input.dispatchEvent(new Event('input'));
}

async function submitEditor(): Promise<void> {
  const form = editorRoot().querySelector('form');
  if (!form) throw new Error('editor form not rendered');
  form.dispatchEvent(new Event('submit'));
  await flushPromises();
}

const mockedList = vi.mocked(api.listDefinitions);
const mockedUpdate = vi.mocked(api.updateDefinition);

function definition(overrides: Partial<api.DefinitionView> = {}): api.DefinitionView {
  return {
    id: 'def-1',
    name: 'e2e-release-target',
    customerId: 'cust-1',
    clusterId: 'cluster-1',
    namespace: 'ns',
    releaseName: 'release-a',
    chartName: 'chart',
    status: 'active',
    version: 3n,
    hpaManaged: false,
    maxEmergencyReplicas: 0,
    createdAt: '2026-09-28T00:00:00Z',
    updatedAt: null,
    promotionMappings: [],
    promotionMappingsViolation: null,
    approvedAnnotationKeys: [],
    approvedAnnotationKeysViolation: null,
    ...overrides,
  };
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/definitions', name: 'Definitions', component: DefinitionsPage }],
  });
  await router.push('/definitions');
  await router.isReady();
  const wrapper = mount(DefinitionsPage, { global: { plugins: [router] } });
  mounted.push(wrapper);
  await flushPromises();
  return wrapper;
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedList.mockReset().mockResolvedValue([definition()]);
  mockedUpdate.mockReset().mockResolvedValue(definition({ version: 4n }));
});

describe('DefinitionsPage', () => {
  it('lists definitions with their mapping count and version', async () => {
    mockedList.mockResolvedValue([
      definition({
        promotionMappings: [
          { workloadKind: 'Deployment', workloadName: 'api', container: 'app', field: 'image', valuesPath: 'image.tag' },
        ],
      }),
    ]);
    const wrapper = await mountPage();

    const row = wrapper.get('[data-testid="definition-row-def-1"]');
    expect(row.text()).toContain('e2e-release-target');
    expect(row.text()).toContain('1 条');
    expect(row.text()).toContain('3');
  });

  // The proto calls an undecodable payload a contract violation: flag it and do not
  // offer an editor that would write the broken shape back.
  it('flags an undecodable mapping and disables its editor', async () => {
    mockedList.mockResolvedValue([definition({ promotionMappingsViolation: '无法解析' })]);
    const wrapper = await mountPage();

    expect(wrapper.get('[data-testid="definitions-violation"]').text()).toContain('契约违反');
    expect(wrapper.get<HTMLButtonElement>('[data-testid="definition-edit-def-1"]').element.disabled).toBe(true);
  });

  it('adds and removes mapping rows in the editor', async () => {
    const wrapper = await mountPage();

    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');
    expect(document.querySelectorAll('[data-testid^="mapping-row-"]')).toHaveLength(0);

    await clickTestId('mapping-add');
    await clickTestId('mapping-add');
    expect(document.querySelectorAll('[data-testid^="mapping-row-"]')).toHaveLength(2);

    await clickTestId('mapping-remove-0');
    expect(document.querySelectorAll('[data-testid^="mapping-row-"]')).toHaveLength(1);
  });

  // TASK-281: the title semantics the hand-rolled `<h2 id="definitions-editor-title">`
  // carried are preserved by AppDialog's own labelled heading.
  it('renders the editor in the shared dialog with a labelled title', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');

    const panel = editorDialogPanel();
    expect(panel.getAttribute('role')).toBe('dialog');
    expect(panel.getAttribute('aria-modal')).toBe('true');
    const titleId = panel.getAttribute('aria-labelledby');
    expect(titleId).toBeTruthy();
    expect(document.getElementById(titleId!)?.textContent).toBe('编辑 e2e-release-target（版本 3）');
  });

  it('refuses to submit a mapping with missing required fields', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');
    await clickTestId('mapping-add');

    await submitEditor();

    expect(document.querySelector('[data-testid="definitions-validation"]')?.textContent).toContain('第 1 行映射缺少必填项');
    expect(mockedUpdate).not.toHaveBeenCalled();
  });

  it('submits the mappings with the version it read and closes the editor', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');
    await clickTestId('mapping-add');

    setEditorInput('kind-0', 'Deployment');
    setEditorInput('name-0', 'api');
    setEditorInput('field-0', 'image');
    setEditorInput('valuesPath-0', 'image.tag');
    await nextTick();
    await submitEditor();

    expect(mockedUpdate).toHaveBeenCalledWith({
      definitionId: 'def-1',
      expectedVersion: 3n,
      namespace: 'ns',
      releaseName: 'release-a',
      chartName: 'chart',
      promotionMappings: [{ workloadKind: 'Deployment', workloadName: 'api', container: '', field: 'image', valuesPath: 'image.tag' }],
    });
    expect(document.querySelector('.definitions__editor')).toBeNull();
    expect(wrapper.get('[data-testid="definitions-notice"]').text()).toContain('版本 4');
  });

  // TASK-214: clearing IS expressible now — the strings carry presence and the mappings
  // travel in a wrapper — so the page submits the clear instead of refusing it.
  it('submits an empty mapping list when the operator clears them all', async () => {
    mockedList.mockResolvedValue([
      definition({
        promotionMappings: [{ workloadKind: 'Deployment', workloadName: 'api', container: '', field: 'image', valuesPath: 'image.tag' }],
      }),
    ]);
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');

    await clickTestId('mapping-remove-0');
    await submitEditor();

    expect(mockedUpdate).toHaveBeenCalledTimes(1);
    const sent = mockedUpdate.mock.calls[0][0];
    // The page's job is to stop refusing and submit the clear; turning it into the
    // presence-carrying wrapper is definition-api.ts's job, asserted in its own test.
    expect(sent.promotionMappings).toEqual([]);
  });

  it('clears a non-empty identifier field by sending an empty string', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');

    setEditorInput('releaseName', '');
    await nextTick();
    await submitEditor();

    expect(mockedUpdate).toHaveBeenCalledTimes(1);
    expect(mockedUpdate.mock.calls[0][0].releaseName).toBe('');
  });

  // After a conflict the list holds the fresh version; the open editor must adopt it,
  // otherwise the hinted retry fails forever with the same stale version.
  it('adopts the refreshed version after a conflict so a retry can succeed', async () => {
    mockedUpdate.mockRejectedValueOnce(new ConnectError('optimistic_lock_conflict: expected 3, current 4', Code.FailedPrecondition));
    mockedList.mockResolvedValue([definition({ version: 4n })]);
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');
    await submitEditor();

    expect(document.querySelector('.definitions__editor')).not.toBeNull();

    // The second attempt must carry the version the server now holds (4), not 3.
    mockedUpdate.mockResolvedValueOnce(definition({ version: 5n }));
    await submitEditor();

    expect(mockedUpdate).toHaveBeenLastCalledWith(expect.objectContaining({ expectedVersion: 4n }));
  });

  it('keeps the editor open and reports a version conflict', async () => {
    mockedUpdate.mockRejectedValue(new ConnectError('optimistic_lock_conflict: expected version 3, current 5', Code.FailedPrecondition));
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');

    await submitEditor();

    expect(wrapper.get('.error-state').text()).toContain('版本冲突');
    expect(document.querySelector('.definitions__editor')).not.toBeNull();
  });

  /*
   * TASK-269. Every mapping control sits in a bare <td>: the column header is a plain
   * <th>, which gives the cell context but NOT an accessible name for the input inside
   * it, and the five inputs of one row are indistinguishable from the next row's five.
   * The assertion is the relation itself (label[for] resolves to the control's id),
   * not the presence of an attribute.
   */
  it('labels every mapping-row input with its column and row', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');
    await clickTestId('mapping-add');
    await clickTestId('mapping-add');

    const root = editorRoot();
    const inputs = Array.from(root.querySelectorAll<HTMLInputElement>('.definitions__mappings tbody input'));
    expect(inputs).toHaveLength(10);

    for (const input of inputs) {
      const id = input.getAttribute('id');
      expect(id, 'an input without an id cannot be pointed at by a label').toBeTruthy();
      expect(root.querySelector(`.definitions__mappings label[for="${id}"]`)?.textContent?.trim()).not.toBe('');
    }

    // Unique per row (two rows cannot share an id)…
    expect(new Set(inputs.map((input) => input.getAttribute('id'))).size).toBe(10);
    // …and the row number is what tells the two rows' Workload 类型 inputs apart.
    expect(root.querySelector('.definitions__mappings label[for$="-0-kind"]')?.textContent?.trim())
      .toBe('Workload 类型（第 1 行）');
    expect(root.querySelector('.definitions__mappings label[for$="-1-kind"]')?.textContent?.trim())
      .toBe('Workload 类型（第 2 行）');
    expect(root.querySelector('.definitions__mappings label[for$="-1-values-path"]')?.textContent?.trim())
      .toBe('Values 路径（第 2 行）');
  });
});
