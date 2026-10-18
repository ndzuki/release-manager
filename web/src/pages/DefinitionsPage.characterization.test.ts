import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';
import { createMemoryHistory, createRouter } from 'vue-router';
import DefinitionsPage from './DefinitionsPage.vue';
import * as api from '@/connect/definition-api';
import { useAuthStore } from '@/stores/auth';

/*
 * TASK-363 characterization tests.
 *
 * The page was 769 lines and is being split into composables + components. These
 * cases pin the behaviour the CURRENT page already has but the original test file
 * did not cover: the filter form, refresh, the three page-level states, the
 * read-only role, create-form reset, and the exact payload the editor builds
 * (trimming / prefill). They are written against the DOM, not against internals, so
 * they keep holding after the split.
 */
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

afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
});

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

function buttonByLabel(wrapper: Awaited<ReturnType<typeof mountPage>>, label: string): HTMLButtonElement {
  const button = wrapper.findAll('button').find((element) => element.text().trim() === label);
  if (!button) throw new Error(`button not found: ${label}`);
  return button.element as HTMLButtonElement;
}

async function clickDocumentTestId(id: string): Promise<void> {
  const element = document.querySelector<HTMLElement>(`[data-testid="${id}"]`);
  if (!element) throw new Error(`missing data-testid=${id}`);
  element.click();
  await nextTick();
}

function editorRoot(): HTMLElement {
  const root = document.querySelector<HTMLElement>('.definitions__editor');
  if (!root) throw new Error('definitions editor dialog not rendered');
  return root;
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

beforeEach(() => {
  setActivePinia(createPinia());
  useAuthStore().$patch({
    user: {
      $typeName: 'auth.v1.SessionUser',
      id: 'admin-1',
      username: 'admin',
      roles: ['release_admin'],
      activeOrgId: 'org-1',
    },
  });
  mockedList.mockReset().mockResolvedValue([definition()]);
  mockedUpdate.mockReset().mockResolvedValue(definition({ version: 4n }));
});

describe('DefinitionsPage filters and refresh (TASK-363)', () => {
  it('sends the trimmed filters and the includeDisabled flag on submit', async () => {
    const wrapper = await mountPage();

    await wrapper.get('input[name="customerId"]').setValue('  cust-1  ');
    await wrapper.get('input[name="clusterId"]').setValue('  cluster-1  ');
    await wrapper.get('input[name="includeDisabled"]').setValue(false);
    await wrapper.get('form.definitions__filters').trigger('submit');
    await flushPromises();

    expect(mockedList).toHaveBeenLastCalledWith({
      customerId: 'cust-1',
      clusterId: 'cluster-1',
      includeDisabled: false,
    });
  });

  it('re-queries with the filters already applied when 刷新 is pressed', async () => {
    const wrapper = await mountPage();
    await wrapper.get('input[name="customerId"]').setValue('cust-1');
    await wrapper.get('form.definitions__filters').trigger('submit');
    await flushPromises();

    buttonByLabel(wrapper, '刷新').click();
    await flushPromises();

    expect(mockedList).toHaveBeenCalledTimes(3);
    expect(mockedList).toHaveBeenLastCalledWith({ customerId: 'cust-1', clusterId: '', includeDisabled: true });
  });

  it('reports the in-flight refresh in the button label', async () => {
    const wrapper = await mountPage();
    let resolveList: ((value: api.DefinitionView[]) => void) | undefined;
    mockedList.mockImplementationOnce(() => new Promise((resolve) => { resolveList = resolve; }));

    buttonByLabel(wrapper, '刷新').click();
    await nextTick();

    expect(buttonByLabel(wrapper, '刷新中…').disabled).toBe(true);
    resolveList?.([definition()]);
    await flushPromises();
    expect(buttonByLabel(wrapper, '刷新').disabled).toBe(false);
  });
});

describe('DefinitionsPage page-level states (TASK-363)', () => {
  it('renders the empty state when the list is empty', async () => {
    mockedList.mockResolvedValue([]);
    const wrapper = await mountPage();

    expect(wrapper.get('.empty-state').text()).toContain('没有发布定义');
    expect(wrapper.find('.definitions__table').exists()).toBe(false);
  });

  it('renders the loading state only while the first read is in flight', async () => {
    let resolveList: ((value: api.DefinitionView[]) => void) | undefined;
    mockedList.mockImplementationOnce(() => new Promise((resolve) => { resolveList = resolve; }));
    const wrapper = await mountPage();

    expect(wrapper.get('.loading-state').text()).toContain('正在读取发布定义…');

    resolveList?.([definition()]);
    await flushPromises();

    expect(wrapper.find('.loading-state').exists()).toBe(false);
    expect(wrapper.find('.definitions__table').exists()).toBe(true);
  });

  it('renders the mapped failure and retries through the ErrorState action', async () => {
    mockedList.mockRejectedValueOnce(new ConnectError('backend down', Code.Unavailable));
    const wrapper = await mountPage();

    const error = wrapper.get('.error-state');
    expect(error.text()).toContain('操作未成功');
    expect(error.text()).toContain('操作失败，请稍后重试');
    expect(error.text()).toContain('code=Unavailable');

    await error.get('.error-state__action').trigger('click');
    await flushPromises();

    expect(mockedList).toHaveBeenCalledTimes(2);
    expect(wrapper.find('.error-state').exists()).toBe(false);
    expect(wrapper.get('[data-testid="definition-row-def-1"]').text()).toContain('e2e-release-target');
  });

  it('marks a disabled definition and a contract violation row without offering illegal actions', async () => {
    mockedList.mockResolvedValue([
      definition({ id: 'def-off', name: 'off-def', status: 'disabled' }),
      definition({ id: 'def-bad', name: 'bad-def', promotionMappingsViolation: '无法解析' }),
    ]);
    const wrapper = await mountPage();

    expect(wrapper.get('[data-testid="definition-row-def-off"]').text()).toContain('已停用');
    expect(wrapper.find('[data-testid="definition-disable-def-off"]').exists()).toBe(false);
    expect(wrapper.get('[data-testid="definition-row-def-bad"]').text()).toContain('无法解析');
    expect(wrapper.get<HTMLButtonElement>('[data-testid="definition-edit-def-bad"]').element.disabled).toBe(true);
  });
});

describe('DefinitionsPage read-only role (TASK-363)', () => {
  it('hides create, edit and disable for a viewer', async () => {
    useAuthStore().$patch({
      user: {
        $typeName: 'auth.v1.SessionUser',
        id: 'viewer-1',
        username: 'viewer',
        roles: ['viewer'],
        activeOrgId: 'org-1',
      },
    });
    const wrapper = await mountPage();

    expect(wrapper.get('[data-testid="definition-row-def-1"]').text()).toContain('e2e-release-target');
    expect(wrapper.find('[data-testid="definitions-create-open"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="definition-edit-def-1"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="definition-disable-def-1"]').exists()).toBe(false);
  });
});

describe('DefinitionsPage editor payload (TASK-363)', () => {
  it('prefills the editor with the values the row already holds', async () => {
    mockedList.mockResolvedValue([
      definition({
        promotionMappings: [
          { workloadKind: 'Deployment', workloadName: 'api', container: 'app', field: 'image', valuesPath: 'image.tag' },
        ],
      }),
    ]);
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');
    await nextTick();

    expect(document.querySelector<HTMLInputElement>('.definitions__editor input[name="namespace"]')?.value).toBe('ns');
    expect(document.querySelector<HTMLInputElement>('.definitions__editor input[name="releaseName"]')?.value).toBe('release-a');
    expect(document.querySelector<HTMLInputElement>('.definitions__editor input[name="chartName"]')?.value).toBe('chart');
    expect(document.querySelector<HTMLInputElement>('.definitions__editor input[name="kind-0"]')?.value).toBe('Deployment');
    expect(document.querySelector<HTMLInputElement>('.definitions__editor input[name="valuesPath-0"]')?.value).toBe('image.tag');
  });

  /*
   * The existing page test ("adopts the refreshed version after a conflict") seeds
   * its list mock BEFORE mount, so the row it edits already carries the refreshed
   * version and the assertion can pass even when the editor never adopts anything.
   * This case keeps the editor genuinely stale: it mounts on version 3 and only then
   * makes the post-conflict reload return version 4.
   */
  it('adopts the refreshed version after a conflict so the retry carries it', async () => {
    const wrapper = await mountPage();
    mockedUpdate.mockRejectedValueOnce(
      new ConnectError('optimistic_lock_conflict: expected 3, current 4', Code.FailedPrecondition),
    );
    mockedList.mockResolvedValue([definition({ version: 4n })]);
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');

    await submitEditor();
    expect(document.querySelector('.app-dialog__title')?.textContent).toBe('编辑 e2e-release-target（版本 4）');

    mockedUpdate.mockResolvedValueOnce(definition({ version: 5n }));
    await submitEditor();

    expect(mockedUpdate).toHaveBeenLastCalledWith(expect.objectContaining({ expectedVersion: 4n }));
  });

  /*
   * TASK-214 made "clear everything" expressible, so the four mapping fields are the
   * only required ones. Each is removed on its own row so a validator that stops
   * checking one of them cannot hide behind the others.
   */
  it.each(['kind', 'name', 'field', 'valuesPath'])('refuses a row whose %s is missing', async (missing) => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');
    await clickDocumentTestId('mapping-add');

    const values: Record<string, string> = {
      kind: 'Deployment',
      name: 'api',
      field: 'image',
      valuesPath: 'image.tag',
    };
    for (const [key, value] of Object.entries(values)) {
      if (key !== missing) setEditorInput(`${key}-0`, value);
    }
    await nextTick();
    await submitEditor();

    expect(document.querySelector('[data-testid="definitions-validation"]')?.textContent).toContain(
      '第 1 行映射缺少必填项',
    );
    expect(mockedUpdate).not.toHaveBeenCalled();
  });

  it('trims every identifier and mapping field before sending', async () => {
    const wrapper = await mountPage();
    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');
    await clickDocumentTestId('mapping-add');

    setEditorInput('namespace', '  ns2  ');
    setEditorInput('releaseName', '  release-b  ');
    setEditorInput('chartName', '  chart2  ');
    setEditorInput('kind-0', '  Deployment  ');
    setEditorInput('name-0', '  api  ');
    setEditorInput('container-0', '  app  ');
    setEditorInput('field-0', '  image  ');
    setEditorInput('valuesPath-0', '  image.tag  ');
    await nextTick();
    await submitEditor();

    expect(mockedUpdate).toHaveBeenCalledWith({
      definitionId: 'def-1',
      expectedVersion: 3n,
      namespace: 'ns2',
      releaseName: 'release-b',
      chartName: 'chart2',
      promotionMappings: [
        { workloadKind: 'Deployment', workloadName: 'api', container: 'app', field: 'image', valuesPath: 'image.tag' },
      ],
    });
  });
});
