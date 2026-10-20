import { create } from '@bufbuild/protobuf';
import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { createMemoryHistory, createRouter } from 'vue-router';
import { Code, ConnectError, type Client } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  GetOperationResponseSchema,
  OperationSchema,
  OperationSnapshotSchema,
  OperationStatus,
  OrchestratorService,
  WatchOperationResponseSchema,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { setOperationClientForTest } from '@/connect/operation-api';
import { useAuthStore } from '@/stores/auth';
import OperationDetailPage from './OperationDetailPage.vue';

/*
 * TASK-394: the operation detail page must never be able to sit on
 * "正在加载 Operation…" forever. Production measured exactly that for a placeholder id:
 * the server opened the WatchOperation stream and then failed it, the store treats a
 * post-open failure as a disconnect (it keeps reconnecting and sets neither
 * `operation` nor `initialError`), and the page had no failure state of its own.
 *
 * These cases pin the page-level contract: a failed diagnosis renders the error state
 * with the stable code and a way back, and a silent stream is bounded by the deadline.
 * The store is intentionally untouched — the page cannot edit it (write scope), so it
 * reads the operation once on the first disconnect and classifies the answer.
 */

const OPERATION_ROUTE =
  '/customers/cust-1/clusters/cluster-1/releases/def-1/operations/missing-id?releaseName=checkout';

function detailRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      {
        path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/operations/:operationId',
        name: 'OperationDetail',
        component: OperationDetailPage,
      },
      {
        path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/operations',
        name: 'OperationList',
        component: { template: '<div />' },
      },
      {
        path: '/customers/:customerId/clusters/:clusterId/releases',
        name: 'ReleaseInventory',
        component: { template: '<div />' },
      },
      { path: '/operations', name: 'OperationCenter', component: { template: '<div />' } },
    ],
  });
}

/*
 * The overrides are intentionally untyped callables: the cases below return rejected
 * async iterables and never-settling promises, shapes the real client type does not
 * admit. The assembled object is then cast to the service client, exactly like the
 * other page tests do.
 */
function operationClient(options: {
  watchOperation: (...args: unknown[]) => unknown;
  getOperation?: (...args: unknown[]) => unknown;
}): Client<typeof OrchestratorService> {
  return {
    watchOperation: options.watchOperation,
    getOperation: options.getOperation ?? vi.fn().mockRejectedValue(
      new ConnectError('operation not found', Code.NotFound),
    ),
  } as unknown as Client<typeof OrchestratorService>;
}

/*
 * An AsyncIterable whose first read rejects: the WatchOperation stream OPENED (the
 * method resolved) and then failed, which is the production shape of the stuck page.
 * A plain async generator would trip `require-yield`, and the code under test never
 * asks for a second value.
 */
function failingStream(error: unknown) {
  return {
    [Symbol.asyncIterator]() {
      return { next: () => Promise.reject(error) };
    },
  };
}

function liveOperation(state: OperationStatus = OperationStatus.RUNNING) {
  const operation = () =>
    create(OperationSchema, {
      operationId: 'missing-id',
      operationType: 'INSTALL',
      state,
      stateVersion: 4n,
      releaseDefinitionId: 'def-1',
      targetRevision: 3,
    });
  return {
    watchOperation: vi.fn(async () =>
      (async function* () {
        yield create(WatchOperationResponseSchema, {
          payload: {
            case: 'snapshot',
            value: create(OperationSnapshotSchema, {
              operation: operation(),
              snapshotSequence: 1n,
              retainedFromSequence: 1n,
            }),
          },
        });
        await new Promise<void>(() => undefined);
      })(),
    ),
    getOperation: vi.fn(async () => create(GetOperationResponseSchema, { operation: operation() })),
  } as unknown as Client<typeof OrchestratorService>;
}

async function mountPage() {
  const router = detailRouter();
  await router.push(OPERATION_ROUTE);
  await router.isReady();
  const wrapper = mount(OperationDetailPage, { global: { plugins: [router] } });
  return wrapper;
}

beforeEach(() => {
  setActivePinia(createPinia());
  useAuthStore().$patch({
    user: {
      $typeName: 'auth.v1.SessionUser',
      id: 'user-1',
      username: 'release-admin',
      roles: ['release_admin'],
      activeOrgId: 'org-1',
    },
    organizations: [{ id: 'org-1', name: 'Org One' }],
  });
});

describe('OperationDetailPage load failure (TASK-394)', () => {
  it('renders the error state when the opened stream fails and GetOperation answers not_found', async () => {
    const getOperation = vi.fn().mockRejectedValue(new ConnectError('operation not found', Code.NotFound));
    setOperationClientForTest(operationClient({
      // The stream OPENS and then fails: this is the production shape of the defect.
      // A rejection before the stream opens already goes through `initialError`; only
      // the post-open failure used to leave the page on the spinner forever.
      watchOperation: vi.fn(async () => failingStream(new ConnectError('operation not found', Code.NotFound))),
      getOperation,
    }));

    const wrapper = await mountPage();

    await vi.waitFor(() =>
      expect(wrapper.find('[data-testid="operation-load-failure"]').exists()).toBe(true),
    );

    const failure = wrapper.get('[data-testid="operation-load-failure"]');
    expect(failure.text()).toContain('操作加载失败');
    // The stable code is part of the visible message, not only of the collapsed details.
    expect(failure.text()).toContain('not_found');
    expect(failure.text()).not.toContain('正在加载 Operation…');
    expect(wrapper.find('.loading-state').exists()).toBe(false);
    expect(getOperation).toHaveBeenCalledWith(expect.objectContaining({ operationId: 'missing-id' }));
    // not_found is terminal, so there is no retry — but there is a way back.
    expect(failure.findAll('button').some((button) => button.text() === '重试')).toBe(false);
    expect(failure.get('a').attributes('href')).toBe(
      '/customers/cust-1/clusters/cluster-1/releases/def-1/operations',
    );
  });

  it('bounds a stream that never answers with the deadline and offers a retry', async () => {
    vi.useFakeTimers();
    try {
      // Never settles: no snapshot, no error, no disconnect — the dead stream the
      // deadline exists for.
      setOperationClientForTest(operationClient({
        watchOperation: vi.fn(() => new Promise(() => undefined)),
      }));

      const wrapper = await mountPage();
      await vi.advanceTimersByTimeAsync(0);
      expect(wrapper.find('[data-testid="operation-load-failure"]').exists()).toBe(false);

      await vi.advanceTimersByTimeAsync(15_000);

      const failure = wrapper.get('[data-testid="operation-load-failure"]');
      expect(failure.text()).toContain('加载超时');
      expect(failure.text()).toContain('load_timeout');
      expect(failure.findAll('button').some((button) => button.text() === '重试')).toBe(true);
      expect(wrapper.find('.loading-state').exists()).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it('renders the live operation instead of the failure state when the diagnosis succeeds', async () => {
    const getOperation = vi.fn().mockResolvedValue(create(GetOperationResponseSchema, {
      operation: create(OperationSchema, {
        operationId: 'missing-id',
        operationType: 'INSTALL',
        state: OperationStatus.RUNNING,
        stateVersion: 4n,
        releaseDefinitionId: 'def-1',
      }),
    }));
    setOperationClientForTest(operationClient({
      watchOperation: vi.fn(async () => failingStream(new ConnectError('stream dropped', Code.Unavailable))),
      getOperation,
    }));

    const wrapper = await mountPage();

    await vi.waitFor(() => expect(wrapper.text()).toContain('操作详情'));
    await vi.waitFor(() => expect(getOperation).toHaveBeenCalledWith(expect.objectContaining({ operationId: 'missing-id' })));
    await flushPromises();

    // The operation exists: the page recovers it through the store instead of showing
    // a failure. `refresh()` is the store's own write path, so the page stays read-only.
    expect(wrapper.find('[data-testid="operation-load-failure"]').exists()).toBe(false);
    await vi.waitFor(() => expect(wrapper.find('.operation-detail__summary').exists()).toBe(true));
  });
});

describe('OperationDetailPage lifecycle steps (TASK-394)', () => {
  async function mountWithState(state: OperationStatus) {
    setOperationClientForTest(liveOperation(state));
    return mountPage();
  }

  function stepStates(wrapper: Awaited<ReturnType<typeof mountWithState>>) {
    return wrapper.findAll('.steps__item').map((item) => ({
      label: item.text(),
      state: item.classes().find((name) => name.startsWith('steps__item--')) ?? '',
    }));
  }

  it('marks preflight current while the pipeline is running', async () => {
    const wrapper = await mountWithState(OperationStatus.PREFLIGHT);
    await vi.waitFor(() => expect(wrapper.findAll('.steps__item')).toHaveLength(4));
    expect(stepStates(wrapper)).toEqual([
      { label: '预检', state: 'steps__item--now' },
      { label: '审批', state: 'steps__item--todo' },
      { label: '下发', state: 'steps__item--todo' },
      { label: '收敛', state: 'steps__item--todo' },
    ]);
  });

  it('moves to dispatch once the operation is handed to the operator', async () => {
    const wrapper = await mountWithState(OperationStatus.RUNNING);
    await vi.waitFor(() => expect(wrapper.findAll('.steps__item')).toHaveLength(4));
    expect(stepStates(wrapper)).toEqual([
      { label: '预检', state: 'steps__item--done' },
      { label: '审批', state: 'steps__item--done' },
      { label: '下发', state: 'steps__item--now' },
      { label: '收敛', state: 'steps__item--todo' },
    ]);
  });

  it('completes every step for a succeeded operation', async () => {
    const wrapper = await mountWithState(OperationStatus.SUCCEEDED);
    await vi.waitFor(() => expect(wrapper.findAll('.steps__item')).toHaveLength(4));
    expect(stepStates(wrapper).map((step) => step.state)).toEqual([
      'steps__item--done',
      'steps__item--done',
      'steps__item--done',
      'steps__item--done',
    ]);
  });
});

describe('OperationDetailPage heading (TASK-394)', () => {
  it('titles the page with the operation type and release name, never the operation id', async () => {
    setOperationClientForTest(liveOperation(OperationStatus.RUNNING));
    const wrapper = await mountPage();
    await vi.waitFor(() => expect(wrapper.get('.page-header__title').text()).toBe('安装操作 · checkout'));

    expect(wrapper.get('.page-header__subtitle').text()).toBe('执行中 · 状态版本 4');
    // The old h1 was the release name and the header printed the whole id below it.
    expect(wrapper.get('.page-header').text()).not.toContain('missing-id');
    // Names first: the release crumb links into the scoped operation list.
    expect(wrapper.get('.page-header__crumbs a[href="/customers/cust-1/clusters/cluster-1/releases/def-1/operations"]').text())
      .toBe('checkout');
  });
});

describe('OperationDetailPage breadcrumbs (TASK-394)', () => {
  it('drops an unnameable level instead of printing its UUID', async () => {
    setOperationClientForTest(liveOperation(OperationStatus.RUNNING));
    const router = detailRouter();
    // A direct URL: no customerName/clusterName/releaseName in the query, and the name
    // stores have nothing for these ids.
    await router.push(
      '/customers/1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d/clusters/2b3c4d5e-6f7a-4b8c-9d0e-1f2a3b4c5d6e'
        + '/releases/b7e1d4c2-0a63-4f58-9c1d-2e7a8b4f6d90/operations/missing-id',
    );
    await router.isReady();
    const wrapper = mount(OperationDetailPage, { global: { plugins: [router] } });
    await vi.waitFor(() => expect(wrapper.get('.page-header__title').text()).toBe('安装操作'));

    const crumbs = wrapper.get('.page-header__crumbs').text();
    expect(crumbs).toBe('操作详情');
    expect(crumbs).not.toContain('1a2b3c4d');
    expect(crumbs).not.toContain('2b3c4d5e');
    expect(crumbs).not.toContain('b7e1d4c2');
  });
});
