import { flushPromises, mount } from '@vue/test-utils';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import ValuesEditorPage from './ValuesEditorPage.vue';

const mocks = vi.hoisted(() => ({
  route: {
    params: { customerId: 'customer-1', clusterId: 'cluster-1', releaseId: 'definition-1' },
    query: { customerName: 'Customer One', clusterName: 'Cluster One', releaseName: 'Release One' },
  },
  auth: {
    user: { id: 'admin-1', roles: ['release_admin'] },
  },
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
    diffResult: { changes: [], hasChanges: false },
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
}));

vi.mock('vue-router', () => ({ useRoute: () => mocks.route }));
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mocks.auth }));
vi.mock('@/stores/valuesEditor', () => ({ useValuesEditorStore: () => mocks.editor }));
vi.mock('@/stores/emergencyAuthorization', () => ({ useEmergencyAuthorizationStore: () => mocks.authorization }));

function mountPage() {
  return mount(ValuesEditorPage, {
    global: {
      stubs: {
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

    expect(wrapper.text()).toContain('创建首个配置 Revision');
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

    expect(wrapper.get('[data-testid="error-state"]').text()).toContain('ValuesRevision 加载失败');
    expect(mocks.editor.editorContent).toBe('replicas: 4');
    await wrapper.get('[data-testid="error-state"] button').trigger('click');
    expect(mocks.editor.load).toHaveBeenCalledTimes(2);
  });
});
