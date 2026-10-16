import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';
import { createMemoryHistory, createRouter } from 'vue-router';
import DefinitionsPage from './DefinitionsPage.vue';
import * as api from '@/connect/definition-api';
import { useDefinitionsStore } from '@/stores/definitions';

vi.mock('@/connect/definition-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return {
    ...original,
    listDefinitions: vi.fn(),
    updateDefinition: vi.fn(),
    createDefinition: vi.fn(),
    disableDefinition: vi.fn(),
  };
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

/* The create dialog and the confirmation are teleported, so their controls are
 * queried in the document (the same pattern as the editor above). */
function createRoot(): HTMLElement {
  const root = document.querySelector<HTMLElement>('.definitions__create');
  if (!root) throw new Error('definitions create dialog not rendered');
  return root;
}

function setCreateInput(name: string, value: string): void {
  const input = document.querySelector<HTMLInputElement>(`.definitions__create input[name="${name}"]`);
  if (!input) throw new Error(`missing create input ${name}`);
  input.value = value;
  input.dispatchEvent(new Event('input'));
}

async function submitCreate(): Promise<void> {
  const form = createRoot().querySelector('form');
  if (!form) throw new Error('create form not rendered');
  form.dispatchEvent(new Event('submit'));
  await flushPromises();
}

function alertDialog(): HTMLElement {
  const matches = Array.from(document.querySelectorAll<HTMLElement>('[role="alertdialog"]'));
  if (matches.length !== 1) throw new Error(`expected exactly one alertdialog, found ${matches.length}`);
  return matches[0]!;
}

function clickButton(root: HTMLElement, label: string): void {
  const button = Array.from(root.querySelectorAll('button')).find(
    (element) => element.textContent?.trim() === label,
  );
  if (!button) throw new Error(`dialog button not found: ${label}`);
  button.click();
}

const mockedList = vi.mocked(api.listDefinitions);
const mockedUpdate = vi.mocked(api.updateDefinition);
const mockedCreate = vi.mocked(api.createDefinition);
const mockedDisable = vi.mocked(api.disableDefinition);

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
  mockedCreate.mockReset().mockResolvedValue(definition({ id: 'def-2', name: 'new-def', version: 1n }));
  mockedDisable.mockReset().mockResolvedValue(definition({ status: 'disabled', version: 4n }));
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

  /*
   * TASK-282: the cancel button is disabled while a save is in flight and Escape
   * now follows it (`closeOnEscape: !store.saving`) instead of staying enabled.
   * Closing mid-save is not harmless: a conflict response adopts the fresh version
   * and re-opens the editor the operator just dismissed.
   */
  it('keeps the editor open on Escape while a save is in flight, and closes it otherwise', async () => {
    let resolveUpdate: ((value: api.DefinitionView) => void) | undefined;
    mockedUpdate.mockImplementationOnce(() => new Promise((resolve) => { resolveUpdate = resolve; }));
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');

    await submitEditor();
    await nextTick();
    expect(document.querySelector('.definitions__editor')).not.toBeNull();
    expect(document.querySelector<HTMLButtonElement>('.definitions__actions button[type="button"]')?.disabled).toBe(true);

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await nextTick();
    expect(document.querySelector('.definitions__editor')).not.toBeNull();

    resolveUpdate?.(definition({ version: 4n }));
    await flushPromises();
    expect(document.querySelector('.definitions__editor')).toBeNull();

    // With no submission in flight Escape still dismisses the editor (unchanged).
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');
    expect(document.querySelector('.definitions__editor')).not.toBeNull();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await nextTick();
    expect(document.querySelector('.definitions__editor')).toBeNull();
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

  // TASK-299: the notice is the shared Toast primitive now. Its testid and copy are
  // what the page's consumers rely on, and a successful update must stay polite
  // (`status`), not become an assertive alert.
  // TASK-304: pin the tone the page asks for — `role` alone cannot see it, because
  // dropping `tone="success"` silently falls back to `info` and still renders `status`.
  // The dismiss control is pinned by its label and by its effect, not by "some button
  // with an aria-label exists".
  it('renders the notice through the Toast primitive without changing its testid or copy', async () => {
    const wrapper = await mountPage();
    useDefinitionsStore().notice = '已更新定义 e2e-release-target（版本 4）';
    await nextTick();

    const notice = wrapper.get('[data-testid="definitions-notice"]');
    expect(notice.text()).toContain('已更新定义 e2e-release-target（版本 4）');
    expect(notice.attributes('role')).toBe('status');
    expect(notice.classes()).toContain('toast--success');
    expect(notice.classes()).not.toContain('toast--info');

    const dismiss = notice.get('.toast__dismiss');
    expect(dismiss.attributes('aria-label')).toBe('关闭通知');
    await dismiss.trigger('click');
    await nextTick();
    expect(wrapper.find('[data-testid="definitions-notice"]').exists()).toBe(false);
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
      /*
       * Two steps on purpose. A missing label yields `undefined` text and the old
       * one-liner `expect(...?.textContent?.trim()).not.toBe('')` PASSED on that —
       * it only caught a present-but-empty label. Existence first, then the text.
       */
      const label = root.querySelector(`.definitions__mappings label[for="${id}"]`);
      expect(label, `no label resolves to the control id ${id}`).not.toBeNull();
      expect(label?.textContent?.trim()).not.toBe('');
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

/*
 * TASK-301: the console could list and edit a definition but not create one, and it
 * could not disable one at all. These cases pin each new entry point, and every
 * negative ("the RPC did not run", "the error is not shown") carries a positive
 * assertion first, so a dialog that never opened cannot make them pass.
 */
describe('DefinitionsPage create (TASK-301)', () => {
  it('creates a definition from the dialog and shows the new row', async () => {
    const wrapper = await mountPage();
    const created = definition({ id: 'def-2', name: 'new-def', version: 1n });
    mockedCreate.mockResolvedValue(created);
    // The list the next fetch returns: the created row has to be in it.
    mockedList.mockResolvedValue([definition(), created]);

    await wrapper.get('[data-testid="definitions-create-open"]').trigger('click');
    expect(createRoot()).toBeTruthy();
    setCreateInput('create-customerId', 'cust-1');
    setCreateInput('create-clusterId', 'cluster-1');
    setCreateInput('create-releaseName', 'new-def');
    await nextTick();
    await submitCreate();

    expect(mockedCreate).toHaveBeenCalledWith({
      customerId: 'cust-1',
      clusterId: 'cluster-1',
      namespace: '',
      releaseName: 'new-def',
      chartName: '',
      enabled: true,
    });
    // The scenario really changed: the new row is rendered and the dialog is gone.
    expect(wrapper.find('[data-testid="definition-row-def-2"]').exists()).toBe(true);
    expect(wrapper.get('[data-testid="definitions-notice"]').text()).toContain('new-def');
    expect(document.querySelector('[data-testid="definitions-create-submit"]')).toBeNull();
  });

  // An empty required field must be refused locally, not sent: the server derives the
  // definition's NAME from release_name, so the request would create a nameless row.
  it('refuses an empty required field before sending', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definitions-create-open"]').trigger('click');

    await submitCreate();

    expect(document.querySelector('[data-testid="definitions-create-validation"]')).not.toBeNull();
    expect(mockedCreate).not.toHaveBeenCalled();
    expect(createRoot()).toBeTruthy();
    wrapper.unmount();
  });

  it('keeps the create dialog open and shows the reason inside it', async () => {
    const wrapper = await mountPage();
    mockedCreate.mockRejectedValue(new ConnectError('definition already exists', Code.AlreadyExists));

    await wrapper.get('[data-testid="definitions-create-open"]').trigger('click');
    setCreateInput('create-customerId', 'cust-1');
    setCreateInput('create-clusterId', 'cluster-1');
    setCreateInput('create-releaseName', 'new-def');
    await nextTick();
    await submitCreate();

    const failure = document.querySelectorAll<HTMLElement>('[data-testid="definitions-create-error"]');
    expect(failure).toHaveLength(1);
    expect(failure[0]?.textContent?.trim()).toBe('相同来源与目标的定义已存在');
    // The copy is announced, not just painted: a screen reader only receives it
    // through the alert role.
    expect(failure[0]?.getAttribute('role')).toBe('alert');
    // Still open, and the submitted values survived the refusal.
    expect(createRoot()).toBeTruthy();
    expect(document.querySelector<HTMLInputElement>('input[name="create-releaseName"]')?.value).toBe('new-def');
  });

  it('associates every create field with its own label', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definitions-create-open"]').trigger('click');

    const root = createRoot();
    const inputs = Array.from(root.querySelectorAll<HTMLInputElement>('.definitions__field input'));
    expect(inputs).toHaveLength(5);
    for (const input of inputs) {
      const id = input.getAttribute('id');
      expect(id, 'a create input without an id cannot be pointed at by a label').toBeTruthy();
      const label = root.querySelector(`.definitions__field label[for="${id}"]`);
      expect(label, `no label resolves to the create input ${id}`).not.toBeNull();
      expect(label?.textContent?.trim()).not.toBe('');
    }
    expect(new Set(inputs.map((input) => input.getAttribute('id'))).size).toBe(5);
  });
});

describe('DefinitionsPage disable (TASK-301)', () => {
  it('arms the confirmation instead of disabling, and names the definition in it', async () => {
    const wrapper = await mountPage();

    const button = wrapper.get<HTMLButtonElement>('[data-testid="definition-disable-def-1"]');
    // VTU skips disabled elements silently, so the enabled state is asserted first.
    expect(button.element.disabled).toBe(false);
    await button.trigger('click');

    expect(mockedDisable).not.toHaveBeenCalled();
    const dialog = alertDialog();
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    const titleId = dialog.getAttribute('aria-labelledby');
    expect(document.getElementById(titleId ?? '')?.textContent).toBe('禁用发布定义');
    expect(dialog.querySelector('.app-dialog__description')?.textContent).toContain('e2e-release-target');
    // The destructive RPC has not run, so the row still offers its disable action.
    expect(wrapper.find('[data-testid="definition-disable-def-1"]').exists()).toBe(true);
  });

  it('cancelling the confirmation executes nothing', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-disable-def-1"]').trigger('click');
    const dialog = alertDialog();

    clickButton(dialog, '取消');
    await flushPromises();

    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
    expect(mockedDisable).not.toHaveBeenCalled();
    expect(wrapper.get('[data-testid="definition-row-def-1"]').text()).toContain('生效中');
  });

  it('disables once, with the version it read, only after confirming', async () => {
    const wrapper = await mountPage();
    mockedDisable.mockResolvedValue(definition({ status: 'disabled', version: 4n }));
    // The list after the disable: the row reports the new status.
    mockedList.mockResolvedValue([definition({ status: 'disabled', version: 4n })]);

    await wrapper.get('[data-testid="definition-disable-def-1"]').trigger('click');
    const dialog = alertDialog();
    clickButton(dialog, '确认禁用');
    await flushPromises();

    expect(mockedDisable).toHaveBeenCalledTimes(1);
    expect(mockedDisable).toHaveBeenCalledWith('def-1', 3n);
    expect(mockedList).toHaveBeenCalledTimes(2);
    expect(document.querySelector('[role="alertdialog"]')).toBeNull();
    expect(wrapper.get('[data-testid="definition-row-def-1"]').text()).toContain('已停用');
    // A disabled definition is terminal: its disable action is no longer offered.
    expect(wrapper.find('[data-testid="definition-disable-def-1"]').exists()).toBe(false);
    expect(wrapper.get('[data-testid="definitions-notice"]').text()).toContain('e2e-release-target');
  });

  /*
   * The page-level <ErrorState> renders behind AppDialog's fixed backdrop, so a failed
   * disable has to put the reason inside the still-open confirmation.
   */
  it('keeps the failure reason inside the still-open dialog and adopts the refreshed version', async () => {
    const wrapper = await mountPage();
    mockedDisable.mockRejectedValueOnce(
      new ConnectError('optimistic_lock_conflict: definition version changed', Code.FailedPrecondition),
    );
    // The row the server now holds: the reload after the conflict returns version 4.
    mockedList.mockResolvedValue([definition({ version: 4n })]);

    await wrapper.get('[data-testid="definition-disable-def-1"]').trigger('click');
    const dialog = alertDialog();
    clickButton(dialog, '确认禁用');
    await flushPromises();

    // The confirmation survived the refusal…
    expect(document.querySelector('[role="alertdialog"]')).toBe(dialog);
    // …and the reason is a descendant of the panel, exactly once.
    const inside = dialog.querySelectorAll<HTMLElement>('[data-testid="definitions-disable-error"]');
    expect(inside).toHaveLength(1);
    expect(inside[0]?.textContent?.trim()).toContain('版本冲突');
    expect(inside[0]?.getAttribute('role')).toBe('alert');
    expect(document.querySelectorAll('[data-testid="definitions-disable-error"]')).toHaveLength(1);
    // A conflict reloaded the list and the row is still active, so a retry is meaningful.
    expect(mockedList).toHaveBeenCalledTimes(2);
    expect(wrapper.get('[data-testid="definition-row-def-1"]').text()).toContain('生效中');

    /*
     * The retry must carry the version the server now holds (4), not the stale 3 the
     * first attempt used: otherwise the hinted retry fails forever.
     */
    mockedDisable.mockResolvedValueOnce(definition({ status: 'disabled', version: 5n }));
    clickButton(alertDialog(), '确认禁用');
    await flushPromises();

    expect(mockedDisable).toHaveBeenCalledTimes(2);
    expect(mockedDisable).toHaveBeenLastCalledWith('def-1', 4n);
  });
});
