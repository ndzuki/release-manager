import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { DiffResult } from '@/types/valuesRevision';
import ValuesEditorPage from './ValuesEditorPage.vue';

const mocks = vi.hoisted(() => ({
  push: vi.fn(async () => undefined),
  route: {
    params: { customerId: 'customer-1', clusterId: 'cluster-1', releaseId: 'definition-1' },
    query: { customerName: 'Customer One', clusterName: 'Cluster One', releaseName: 'Release One' },
  },
  auth: {
    user: { id: 'admin-1', roles: ['release_admin'] },
  },
  /*
   * TASK-394: the breadcrumbs resolve NAMES through `useScopeNames`; the real composable
   * reads the customer/cluster stores, which would need a Pinia instance this suite does not
   * create. The stub keeps the same ref shape and lets a case make a lookup "fail" (the name
   * falls back to the identifier) to prove the page omits that crumb instead of printing a
   * UUID.
   */
  scope: { customerName: 'Customer One', clusterName: 'Cluster One' },
  authorization: {
    canApproveValuesRevision: false,
    load: vi.fn(async () => undefined),
    reset: vi.fn(),
  },
  editor: {
    currentRevision: null as null | { createdByUserId: string; status: string },
    parentRevision: null as null | { status: string },
    editorContent: '# Paste or edit your values.yaml here\n{}',
    editorLanguage: 'yaml',
    canApprove: false,
    canEdit: true,
    loading: false,
    saving: false,
    approving: false,
    discarding: false,
    convergenceMode: false,
    lockedPaths: [] as string[],
    preparedTaskIds: [] as string[],
    error: null as string | null,
    canonicalCurrent: {} as unknown | null,
    validationIssue: null,
    diffResult: { changes: [], hasChanges: false } as DiffResult,
    secretRefs: [],
    availableSecrets: [],
    secretRefsError: null,
    saveDisabled: false,
    restoredDraft: false,
    toast: null,
    showConflictDialog: false,
    resetScope: vi.fn(),
    setEditable: vi.fn(),
    load: vi.fn(async () => undefined),
    loadConvergence: vi.fn(async () => undefined),
    reloadParent: vi.fn(async () => undefined),
    setEditorLanguage: vi.fn(),
    setEditorContent: vi.fn(),
    updateSecretRef: vi.fn(),
    addSecretRef: vi.fn(),
    removeSecretRef: vi.fn(),
    save: vi.fn(async () => true),
    submit: vi.fn(async () => true),
    approve: vi.fn(async () => true),
    reject: vi.fn(async () => true),
    discard: vi.fn(async () => true),
    dispose: vi.fn(),
  },
  /*
   * TASK-267: a reactive view of the same stub. The page reads `editor.toast`
   * from a `v-if`; only a reactive source lets a test observe the "dismiss, then
   * succeed again" sequence the real store drives. Kept on the hoisted object so
   * both the mock factory and the cases can reach it.
   */
  editorProxy: null as null | { toast: string | null },
}));

vi.mock('vue-router', () => ({ useRoute: () => mocks.route, useRouter: () => ({ push: mocks.push }) }));
vi.mock('@/composables/useScopeNames', async () => {
  const { computed, reactive } = await import('vue');
  const scope = reactive(mocks.scope);
  return {
    useScopeNames: () => ({
      customerId: computed(() => 'customer-1'),
      clusterId: computed(() => 'cluster-1'),
      customerName: computed(() => scope.customerName),
      clusterName: computed(() => scope.clusterName),
    }),
  };
});
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mocks.auth }));
vi.mock('@/stores/valuesEditor', async () => {
  // TASK-267: the stub is reactive so the page's `v-if`/`@dismiss` wiring can be
  // observed across renders; the store's own behaviour is covered by
  // stores/valuesEditor.test.ts and is not the subject here.
  const { reactive } = await import('vue');
  mocks.editorProxy = reactive(mocks.editor);
  return { useValuesEditorStore: () => mocks.editorProxy };
});
vi.mock('@/stores/emergencyAuthorization', () => ({ useEmergencyAuthorizationStore: () => mocks.authorization }));

function mountPage() {
  return mount(ValuesEditorPage, {
    global: {
      stubs: {
        RouterLink: true,
        ValuesEditorSkeleton: { template: '<div data-testid="values-skeleton">Loading values</div>' },
        ValuesCodeEditor: {
          props: ['modelValue'],
          template: '<pre data-testid="values-editor">{{ modelValue }}</pre>',
        },
        ValuesDiffPanel: true,
        SecretRefEditor: true,
        ValuesRevisionActions: true,
        ValuesConflictDialog: true,
        RejectRevisionDialog: true,
        ErrorState: {
          props: ['title', 'message', 'actionLabel'],
          emits: ['action'],
          template: '<section data-testid="error-state"><h2>{{ title }}</h2><p>{{ message }}</p><button @click="$emit(\'action\')">{{ actionLabel }}</button></section>',
        },
      },
    },
  });
}

describe('ValuesEditorPage', () => {
  beforeEach(() => {
    Object.assign(mocks.editor, {
      currentRevision: null,
      parentRevision: null,
      editorContent: '# Paste or edit your values.yaml here\n{}',
      loading: false,
      error: null,
      canonicalCurrent: {},
      restoredDraft: false,
      toast: null,
      showConflictDialog: false,
    });
    vi.clearAllMocks();
  });

  it('renders the first-revision empty state and safe template', async () => {
    const wrapper = mountPage();
    await flushPromises();

    expect(wrapper.text()).toContain('创建首个配置版本');
    expect(wrapper.get('[data-testid="values-editor"]').text()).toContain('# Paste or edit your values.yaml here');
  });

  it('renders a skeleton during the initial revision request', async () => {
    mocks.editor.loading = true;

    const wrapper = mountPage();
    await flushPromises();

    expect(wrapper.find('[data-testid="values-skeleton"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="values-editor"]').exists()).toBe(false);
  });

  it('renders a retryable load error without replacing the editor draft', async () => {
    mocks.editor.error = '网络或 Operator 暂不可用，请检查连接后重试';
    mocks.editor.canonicalCurrent = null;
    mocks.editor.editorContent = 'replicas: 4';

    const wrapper = mountPage();
    await flushPromises();

    expect(wrapper.get('[data-testid="error-state"]').text()).toContain('Values 版本加载失败');
    expect(mocks.editor.editorContent).toBe('replicas: 4');
    await wrapper.get('[data-testid="error-state"] button').trigger('click');
    expect(mocks.editor.load).toHaveBeenCalledTimes(2);
  });
});

// AC-055-11: the reject action opens the reason dialog, and the reason the user
// typed reaches the store. Before this the dialog existed but was never mounted,
// so reject() was called with no reason at all.
describe('ValuesEditorPage reject reason (AC-055-11)', () => {
  function mountWithEmittingStubs() {
    return mount(ValuesEditorPage, {
      global: {
        stubs: {
          // PageHeader renders a RouterLink for a linked crumb; this suite mounts the page
          // outside a router (vue-router is mocked), so the component must be stubbed.
          RouterLink: true,
          ValuesEditorSkeleton: true,
          ValuesCodeEditor: { props: ['modelValue'], template: '<pre>{{ modelValue }}</pre>' },
          ValuesDiffPanel: true,
          SecretRefEditor: true,
          ValuesConflictDialog: true,
          ErrorState: true,
          ValuesRevisionActions: {
            emits: ['approve', 'reject'],
            template: '<div><button data-testid="reject" @click="$emit(\'reject\')">reject</button></div>',
          },
          RejectRevisionDialog: {
            props: ['submitting'],
            emits: ['submit', 'close'],
            template: '<div data-testid="reject-dialog"><button data-testid="submit" @click="$emit(\'submit\', \'needs a smaller image\')">submit</button></div>',
          },
        },
      },
    });
  }

  beforeEach(() => {
    Object.assign(mocks.editor, {
      currentRevision: { id: 'rev-1', status: 'pending_approval', stateVersion: '1' },
      parentRevision: null,
      editorContent: '{}',
      loading: false,
      error: null,
      canonicalCurrent: {},
      restoredDraft: false,
      toast: null,
      showConflictDialog: false,
      approving: false,
      discarding: false,
      saving: false,
      saveDisabled: false,
      preparedTaskIds: [],
      reject: vi.fn().mockResolvedValue(true),
    });
    vi.clearAllMocks();
  });

  it('opens the dialog on reject and forwards the typed reason to the store', async () => {
    const wrapper = mountWithEmittingStubs();
    await flushPromises();

    expect(wrapper.find('[data-testid="reject-dialog"]').exists()).toBe(false);

    await wrapper.find('[data-testid="reject"]').trigger('click');
    expect(wrapper.find('[data-testid="reject-dialog"]').exists()).toBe(true);

    await wrapper.find('[data-testid="submit"]').trigger('click');
    await flushPromises();

    expect(mocks.editor.reject).toHaveBeenCalledWith('needs a smaller image');
    expect(wrapper.find('[data-testid="reject-dialog"]').exists()).toBe(false);
  });
});

// AC-055-15: a successful Reject or Discard returns to the convergence task
// list, and merely leaving the page never discards.
describe('ValuesEditorPage return to task list (AC-055-15)', () => {
  function mountWithActions() {
    return mount(ValuesEditorPage, {
      global: {
        stubs: {
          // PageHeader renders a RouterLink for a linked crumb; this suite mounts the page
          // outside a router (vue-router is mocked), so the component must be stubbed.
          RouterLink: true,
          ValuesEditorSkeleton: true,
          ValuesCodeEditor: { props: ['modelValue'], template: '<pre>{{ modelValue }}</pre>' },
          ValuesDiffPanel: true,
          SecretRefEditor: true,
          ValuesConflictDialog: true,
          ErrorState: true,
          ConvergenceLockedPathsPanel: true,
          ValuesRevisionActions: {
            emits: ['approve', 'reject', 'discard'],
            template: '<div><button data-testid="reject" @click="$emit(\'reject\')">r</button><button data-testid="discard" @click="$emit(\'discard\')">d</button></div>',
          },
          RejectRevisionDialog: {
            props: ['submitting'],
            emits: ['submit', 'close'],
            template: '<div data-testid="reject-dialog"><button data-testid="submit" @click="$emit(\'submit\', \'why\')">s</button></div>',
          },
        },
      },
    });
  }

  beforeEach(() => {
    Object.assign(mocks.editor, {
      currentRevision: { id: 'rev-1', status: 'pending_approval', stateVersion: '1' },
      parentRevision: null,
      editorContent: '{}',
      loading: false,
      error: null,
      canonicalCurrent: {},
      restoredDraft: false,
      toast: null,
      showConflictDialog: false,
      approving: false,
      discarding: false,
      saving: false,
      saveDisabled: false,
      preparedTaskIds: [],
      reject: vi.fn(async () => true),
      discard: vi.fn(async () => true),
    });
    vi.clearAllMocks();
  });

  it('navigates to the convergence tasks after a successful reject', async () => {
    const wrapper = mountWithActions();
    await flushPromises();
    await wrapper.find('[data-testid="reject"]').trigger('click');
    await wrapper.find('[data-testid="submit"]').trigger('click');
    await flushPromises();

    expect(mocks.push).toHaveBeenCalledWith({
      name: 'ConvergenceTasks',
      params: { customerId: 'customer-1', clusterId: 'cluster-1', releaseId: 'definition-1' },
    });
  });

  it('does not navigate when the reject fails', async () => {
    mocks.editor.reject.mockResolvedValue(false);
    const wrapper = mountWithActions();
    await flushPromises();
    await wrapper.find('[data-testid="reject"]').trigger('click');
    await wrapper.find('[data-testid="submit"]').trigger('click');
    await flushPromises();

    expect(mocks.push).not.toHaveBeenCalled();
  });

  it('navigates to the convergence tasks after a successful discard', async () => {
    const wrapper = mountWithActions();
    await flushPromises();
    await wrapper.find('[data-testid="discard"]').trigger('click');
    // AppDialog teleports the overlay to body, so confirm through the real DOM.
    const dialog = document.querySelector<HTMLElement>('.app-dialog__panel');
    expect(dialog, 'the discard confirmation must be rendered').not.toBeNull();
    const confirm = Array.from(dialog!.querySelectorAll('button'))
      .find((element) => element.textContent?.trim() === '确认丢弃');
    expect(confirm, 'the discard confirmation must be rendered').toBeTruthy();
    confirm!.click();
    await flushPromises();

    expect(mocks.push).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'ConvergenceTasks' }),
    );
  });
});

/*
 * TASK-267. The operation-result notice is the shared Toast primitive now. Toast
 * keeps its own `visible` flag and hides itself on dismiss, and it never sets that
 * flag back: a notice handed to the SAME instance after a dismissal renders nothing
 * (the documented single-instance limit, pinned in Toast.test.ts). The page's half
 * of the contract is `@dismiss="editor.toast = null"` — clearing the source drops
 * the `v-if`, so the NEXT success mounts a fresh instance.
 *
 * That is exactly what a browser check of "the box disappears" cannot see, so it is
 * pinned here as a page-level sequence: success -> dismiss -> success again. The
 * store is this file's standing stub (stores/valuesEditor.test.ts owns its real
 * behaviour); the mock's `save` mirrors the real one, which sets this exact copy on
 * success (stores/valuesEditor.ts).
 */
describe('ValuesEditorPage notice lifetime (TASK-267)', () => {
  const pages: Array<{ unmount: () => void }> = [];

  function mountWithSaveAction() {
    const wrapper = mount(ValuesEditorPage, {
      global: {
        stubs: {
          // PageHeader renders a RouterLink for a linked crumb; this suite mounts the page
          // outside a router (vue-router is mocked), so the component must be stubbed.
          RouterLink: true,
          ValuesEditorSkeleton: true,
          ValuesCodeEditor: { props: ['modelValue'], template: '<pre data-testid="values-editor">{{ modelValue }}</pre>' },
          ValuesDiffPanel: true,
          SecretRefEditor: true,
          ValuesConflictDialog: true,
          ErrorState: true,
          ConvergenceLockedPathsPanel: true,
          RejectRevisionDialog: true,
          ValuesRevisionActions: {
            emits: ['save'],
            template: '<button data-testid="save" @click="$emit(\'save\')">save</button>',
          },
        },
      },
    });
    pages.push(wrapper);
    return wrapper;
  }

  beforeEach(() => {
    Object.assign(mocks.editor, {
      currentRevision: null,
      parentRevision: null,
      editorContent: '{}',
      loading: false,
      error: null,
      canonicalCurrent: {},
      restoredDraft: false,
      toast: null,
      showConflictDialog: false,
      saving: false,
      saveDisabled: false,
    });
    vi.clearAllMocks();
    mocks.editor.save.mockImplementation(async () => {
      mocks.editorProxy!.toast = '已保存为 Draft';
      return true;
    });
  });

  afterEach(() => {
    for (const wrapper of pages.splice(0)) wrapper.unmount();
    document.body.innerHTML = '';
  });

  it('shows the confirmation again after it is dismissed and the next save succeeds', async () => {
    const wrapper = mountWithSaveAction();
    await flushPromises();

    // 1. Success renders the notice (the positive backing for every absence below).
    await wrapper.get('[data-testid="save"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-testid="values-editor-toast"]').text()).toContain('已保存为 Draft');

    // 2. Dismissing removes it from the DOM.
    await wrapper.get('[data-testid="values-editor-toast-dismiss"]').trigger('click');
    await flushPromises();
    expect(wrapper.find('[data-testid="values-editor-toast"]').exists()).toBe(false);

    // 3. The next success must mount a fresh Toast. Without the page clearing
    //    `editor.toast` on dismiss, the retained instance stays hidden and this is
    //    where the regression shows up.
    await wrapper.get('[data-testid="save"]').trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-testid="values-editor-toast"]').text()).toContain('已保存为 Draft');
  });
});

/*
 * TASK-394 (direction C). The page moved onto the shared PageHeader and the two-column
 * editor | diff row. These pin the contract the migration must preserve: names (never
 * identifiers) in the crumbs, a language switch that really reaches the store, the header
 * save entry, the diff data source, and the disabled read-only state.
 */
describe('ValuesEditorPage direction C surface (TASK-394)', () => {
  function mountWithDiffStub() {
    return mount(ValuesEditorPage, {
      global: {
        stubs: {
          // PageHeader renders a RouterLink for a linked crumb; this suite mounts the page
          // outside a router (vue-router is mocked), so the component must be stubbed.
          RouterLink: true,
          ValuesEditorSkeleton: true,
          ValuesCodeEditor: {
            props: ['modelValue', 'language', 'readOnly'],
            template: '<pre data-testid="values-editor">{{ modelValue }}</pre>',
          },
          ValuesDiffPanel: {
            props: ['result'],
            template: '<pre data-testid="values-diff">{{ JSON.stringify(result) }}</pre>',
          },
          SecretRefEditor: true,
          ValuesRevisionActions: true,
          ValuesConflictDialog: true,
          RejectRevisionDialog: true,
          ErrorState: true,
        },
      },
    });
  }

  beforeEach(() => {
    Object.assign(mocks.editor, {
      currentRevision: null,
      parentRevision: null,
      editorContent: '{}',
      editorLanguage: 'yaml',
      loading: false,
      error: null,
      canonicalCurrent: {},
      restoredDraft: false,
      toast: null,
      showConflictDialog: false,
      saving: false,
      saveDisabled: false,
      diffResult: { changes: [], hasChanges: false } as DiffResult,
    });
    mocks.auth.user = { id: 'admin-1', roles: ['release_admin'] };
    Object.assign(mocks.scope, { customerName: 'Customer One', clusterName: 'Cluster One' });
    vi.clearAllMocks();
  });

  it('heads the page with the release name and passes scope names as crumbs', async () => {
    const wrapper = mountWithDiffStub();
    await flushPromises();

    expect(wrapper.get('h1').text()).toBe('Release One Values');
    const crumbs = wrapper.get('nav[aria-label="面包屑"]').text();
    expect(crumbs).toContain('Customer One');
    expect(crumbs).toContain('Cluster One');
    expect(crumbs).toContain('Release One');
    expect(crumbs).not.toContain('definition-1');
    expect(wrapper.text()).toContain('编辑规范化 Values 内容，通过 Secret 引用读取集群 Secret。');
  });

  it('omits a crumb whose resolved name is the identifier instead of printing a UUID', async () => {
    Object.assign(mocks.scope, { customerName: 'customer-1', clusterName: 'cluster-1' });

    const wrapper = mountWithDiffStub();
    await flushPromises();

    const crumbs = wrapper.get('nav[aria-label="面包屑"]').text();
    expect(crumbs).not.toContain('customer-1');
    expect(crumbs).not.toContain('cluster-1');
    expect(crumbs).toContain('Release One');
  });

  /*
   * TASK-394 (consistency wave 7): the editor panel's label was the console's last
   * hand-written `.eyebrow` in the page layer. It is a section title now — same catalog key,
   * direction-C type — while the h2 keeps the region's heading semantics and the page keeps
   * exactly one h1.
   */
  it('labels the editor panel with a section title instead of the retired eyebrow', async () => {
    const wrapper = mountWithDiffStub();
    await flushPromises();

    const panel = wrapper.get('.values-panel');
    expect(panel.find('.eyebrow').exists()).toBe(false);
    expect(panel.findAll('.values-panel__header h2')).toHaveLength(1);
    // The panel names its CONTENT, so it never repeats the page title verbatim.
    expect(panel.get('.values-panel__header h2').text()).toBe('Values 内容');
    expect(panel.get('h2#values-editor-title').text()).toBe('Values 内容');
    expect(wrapper.findAll('h1')).toHaveLength(1);
  });

  it('switches the editor language through the segmented control', async () => {
    const wrapper = mountWithDiffStub();
    await flushPromises();

    const yaml = wrapper.get('[data-testid="values-language-yaml"]');
    const json = wrapper.get('[data-testid="values-language-json"]');
    expect(yaml.attributes('aria-pressed')).toBe('true');
    expect(json.attributes('aria-pressed')).toBe('false');

    await json.trigger('click');
    expect(mocks.editor.setEditorLanguage).toHaveBeenCalledWith('json');
  });

  it('saves a new revision from the header action', async () => {
    const wrapper = mountWithDiffStub();
    await flushPromises();

    await wrapper.get('[data-testid="values-save-new"]').trigger('click');
    expect(mocks.editor.save).toHaveBeenCalledTimes(1);
  });

  it('disables the language switch and the save action for a read-only role', async () => {
    mocks.auth.user = { id: 'viewer-1', roles: ['viewer'] };

    const wrapper = mountWithDiffStub();
    await flushPromises();

    expect(wrapper.get('[data-testid="values-language-yaml"]').attributes('disabled')).toBeDefined();
    expect(wrapper.get('[data-testid="values-language-json"]').attributes('disabled')).toBeDefined();
    expect(wrapper.get('[data-testid="values-save-new"]').attributes('disabled')).toBeDefined();
  });

  it('feeds the store diff result into the side-by-side panel', async () => {
    mocks.editor.diffResult = {
      changes: [{ path: '.replicas', kind: 'modified', oldValue: 1, newValue: 3 }],
      hasChanges: true,
    };

    const wrapper = mountWithDiffStub();
    await flushPromises();

    const panel = wrapper.get('[data-testid="values-diff"]').text();
    expect(panel).toContain('.replicas');
    expect(panel).toContain('"newValue":3');
  });
});
