import { mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { nextTick, reactive } from 'vue';
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
 * The route params are empty by default on purpose: the page's scope watch then returns
 * before issuing requests, and each test drives the store state it needs directly. The
 * route object is REACTIVE because TASK-394 moved the breadcrumbs into PageHeader's
 * `crumbs` and the trail must be re-derived when the route changes — a plain object would
 * hide a `computed` regression (the crumb targets would freeze at their first value).
 */
const routeState = reactive({
  params: {} as Record<string, string>,
  query: {} as Record<string, string>,
  path: '/',
  fullPath: '/',
  name: undefined,
  hash: '',
  matched: [],
  meta: {},
  redirectedFrom: undefined,
});

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router');
  return {
    ...actual,
    useRoute: () => routeState,
    useRouter: () => ({ push: vi.fn() }),
  };
});

/*
 * A RouterLink stub that renders its `to` target, so a crumb's LINK TARGET can be asserted
 * instead of only its label. The real RouterLink needs a router instance the state tests
 * deliberately do not create.
 */
const RouterLinkStub = {
  props: { to: { type: [String, Object] } },
  template: '<a :data-to="JSON.stringify(to)"><slot /></a>',
};

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

// Every case starts from an empty route: the state tests depend on the scope watch
// returning early, and the header cases set their own params explicitly.
beforeEach(() => {
  routeState.params = {};
  routeState.query = {};
});

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

/*
 * TASK-394 (consistency wave 5): the page used to build its own breadcrumb and print the
 * release-definition id inside the h1. PageHeader forbids a trail/title built from route
 * params, but the id is the reader's only clue to which definition they are converging —
 * so the contract is: title without the id, identity behind a truncated CopyableHash, and
 * a crumb trail whose LINK TARGET follows the route.
 */
describe('ConvergenceTasksPage header (TASK-394)', () => {
  const DEFINITION_ID = '12345678-90ab-cdef-1234-567890abcdef';

  beforeEach(() => setActivePinia(createPinia()));

  async function mountPage() {
    const authorization = useEmergencyAuthorizationStore();
    authorization.configure({ loadSnapshot: async () => snapshot() });
    await authorization.load('org-1', 'cust-1');
    return mount(ConvergenceTasksPage, { global: { stubs: { RouterLink: RouterLinkStub } } });
  }

  it('keeps the definition id out of the heading and behind a truncated CopyableHash', async () => {
    routeState.params = { customerId: 'cust-1', clusterId: 'cluster-1', releaseId: DEFINITION_ID };

    const wrapper = await mountPage();

    expect(wrapper.get('h1').text()).toBe('待收敛任务');
    expect(wrapper.text()).not.toContain(DEFINITION_ID);
    const hash = wrapper.get('.copyable-hash__value');
    expect(hash.text()).toBe('12345678…');
    // The full value stays reachable: `title` for sighted users, the copy control for the rest.
    expect(hash.attributes('title')).toBe(DEFINITION_ID);
    expect(wrapper.find('.copyable-hash__button').exists()).toBe(true);
  });

  it('re-derives the crumb link target when the route changes', async () => {
    routeState.params = { customerId: 'cust-1', clusterId: 'cluster-1', releaseId: DEFINITION_ID };
    const wrapper = await mountPage();

    const link = wrapper.get('nav a');
    expect(link.attributes('data-to')).toContain('"name":"ReleaseInventory"');
    expect(link.attributes('data-to')).toContain('"customerId":"cust-1"');
    expect(link.attributes('data-to')).toContain('"clusterId":"cluster-1"');
    // The last crumb is this page: never a link, marked aria-current.
    expect(wrapper.get('nav [aria-current="page"]').text()).toBe('收敛任务');

    routeState.params = { customerId: 'cust-2', clusterId: 'cluster-2', releaseId: DEFINITION_ID };
    await nextTick();

    const updated = wrapper.get('nav a').attributes('data-to');
    expect(updated).toContain('"customerId":"cust-2"');
    expect(updated).toContain('"clusterId":"cluster-2"');
  });
});
