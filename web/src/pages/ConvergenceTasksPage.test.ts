import { mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';
import { useConvergenceSelectionStore } from '@/stores/convergenceSelection';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import type { EmergencyAuthorizationProjection } from '@/connect/emergency-api';
import type { ConvergenceTaskDisplay } from '@/features/emergency/model';
import ConvergenceTasksPage from './ConvergenceTasksPage.vue';

/*
 * UX-035 (W15): the page rendered the task table while `selection.load` was still
 * running, so an in-flight read looked like "no convergence tasks". The states below
 * are the observable contract: loading, failure, empty and populated are distinct.
 *
 * The route params are empty on purpose: the page's scope watch then returns before
 * issuing requests, and each test drives the store state it needs directly.
 */
vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router');
  return {
    ...actual,
    useRoute: () => ({
      params: {}, query: {}, path: '/', fullPath: '/', name: undefined,
      hash: '', matched: [], meta: {}, redirectedFrom: undefined,
    }),
    useRouter: () => ({ push: vi.fn() }),
  };
});

function snapshot(): EmergencyAuthorizationProjection {
  return {
    organizationId: 'org-1',
    customerId: 'cust-1',
    bindingActive: true,
    customerActive: true,
    role: 'release_admin',
    canExecuteEmergency: true,
    canResolveEmergency: true,
    canCreateValuesRevision: true,
    canApproveValuesRevision: true,
    sourceVersion: 1n,
    policyVersion: 1n,
    checkpoint: 1n,
    fresh: true,
    actorId: 'user-1',
    emergencyChangeEnabled: true,
  };
}

function task(overrides: Partial<ConvergenceTaskDisplay> = {}): ConvergenceTaskDisplay {
  return {
    taskId: 'task-1',
    operationId: 'op-1',
    opType: 'SET_REPLICAS',
    targetSummary: 'Deployment ns/api',
    submittedAt: '2026-10-18T00:00:00.000Z',
    reasonDisplay: '扩缩容紧急变更',
    promotionPaths: ['spec.replicas'],
    activeRevisionId: '',
    activeRevisionStatus: '',
    selectable: true,
    incompatibilityReason: '',
    ...overrides,
  };
}

describe('ConvergenceTasksPage states (UX-035)', () => {
  beforeEach(() => setActivePinia(createPinia()));

  async function mountPage() {
    const authorization = useEmergencyAuthorizationStore();
    authorization.configure({ loadSnapshot: async () => snapshot() });
    await authorization.load('org-1', 'cust-1');

    const selection = useConvergenceSelectionStore();
    const wrapper = mount(ConvergenceTasksPage, { global: { stubs: { RouterLink: true } } });
    return { wrapper, selection };
  }

  it('shows the loading state instead of an empty task table while the read is in flight', async () => {
    const { wrapper, selection } = await mountPage();
    selection.loading = true;
    await nextTick();

    expect(wrapper.text()).toContain('正在加载收敛任务');
    expect(wrapper.find('.task-table').exists()).toBe(false);
  });

  it('shows the error state when the read fails', async () => {
    const { wrapper, selection } = await mountPage();
    selection.loading = false;
    selection.loadError = { code: 'unavailable', message: '收敛任务读取失败', retryable: true, typed: false };
    await nextTick();

    expect(wrapper.get('[role="alert"]').text()).toContain('收敛任务读取失败');
    expect(wrapper.find('.task-table').exists()).toBe(false);
  });

  it('shows the empty state when the read succeeded with no tasks', async () => {
    const { wrapper, selection } = await mountPage();
    selection.loading = false;
    selection.loadError = null;
    selection.tasks = [];
    await nextTick();

    expect(wrapper.text()).toContain('没有待收敛任务');
    expect(wrapper.find('.task-table').exists()).toBe(false);
  });

  it('renders the task table once the read produced tasks', async () => {
    const { wrapper, selection } = await mountPage();
    selection.loading = false;
    selection.loadError = null;
    selection.tasks = [task()];
    await nextTick();

    expect(wrapper.find('.task-table').exists()).toBe(true);
    expect(wrapper.text()).toContain('Deployment ns/api');
    expect(wrapper.text()).not.toContain('没有待收敛任务');
  });
});
