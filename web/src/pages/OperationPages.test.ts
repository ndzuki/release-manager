import { create } from '@bufbuild/protobuf';
import { mount, type VueWrapper } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { createMemoryHistory, createRouter, type Router } from 'vue-router';
import { Code, ConnectError } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Client } from '@connectrpc/connect';
import {
  BundleService,
  BundleSummarySchema,
  CancelOperationResponseSchema,
  CreateOperationResponseSchema,
  GetOperationResponseSchema,
  ListBundlesResponseSchema,
  OperationSchema,
  OperationSnapshotSchema,
  OperationStatus,
  OrchestratorService,
  WatchOperationResponseSchema,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { BundleStatus } from '@/gen/common/v1/domain_pb';
import { setOperationClientForTest } from '@/connect/operation-api';
import OperationCreatePage from './OperationCreatePage.vue';
import { useOperationTimelineStore } from '@/stores/operationTimeline';
import OperationDetailPage from './OperationDetailPage.vue';

interface TestClients {
  operations: Client<typeof OrchestratorService>;
  bundles: Client<typeof BundleService>;
}

function optionsClients(): TestClients {
  return {
    operations: {
      createOperation: vi.fn().mockResolvedValue(create(CreateOperationResponseSchema, {
        operationId: 'op-created', state: 'preflight', preflightId: 'pf-created',
      })),
    } as unknown as Client<typeof OrchestratorService>,
    bundles: {
      listBundles: vi.fn().mockResolvedValue(create(ListBundlesResponseSchema, {
        bundles: [create(BundleSummarySchema, {
          id: 'bundle-1',
          name: 'app',
          digest: { algorithm: 'sha256', value: 'sha256:bundle' },
          status: BundleStatus.VALIDATED,
          chartRef: 'oci://registry/app',
          chartVersion: '1.0.0',
          chartDigest: 'sha256:chart',
          images: [{ ref: 'registry/app:v1', digest: 'sha256:image', valuesPath: 'image' }],
        })],
      })),
    } as unknown as Client<typeof BundleService>,
  };
}

async function mountCreatePage(clients: TestClients) {
  setOperationClientForTest(clients.operations, clients.bundles);
  const pinia = createPinia();
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/operations/new', name: 'OperationCreate', component: OperationCreatePage },
      { path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/operations/:operationId', name: 'OperationDetail', component: OperationDetailPage },
      { path: '/customers/:customerId/clusters/:clusterId/releases', name: 'ReleaseInventory', component: { template: '<div />' } },
    ],
  });
  await router.push('/customers/cust-1/clusters/cluster-1/releases/def-1/operations/new?currentRevision=5');
  await router.isReady();
  const wrapper = mount(OperationCreatePage, {
    global: { plugins: [pinia, router] },
  });
  await vi.waitFor(() => expect(wrapper.text()).toContain('app@1.0.0'));
  return { wrapper, router };
}

async function fillBundleAndValues(wrapper: VueWrapper): Promise<void> {
  await wrapper.find('select').setValue('bundle-1');
  await wrapper.find('input[aria-label="ValuesRevision ID"]').setValue('vr-1');
}

function emptyOptionsClients(): TestClients {
  return {
    operations: {} as unknown as Client<typeof OrchestratorService>,
    bundles: {
      listBundles: vi.fn().mockResolvedValue(create(ListBundlesResponseSchema)),
    } as unknown as Client<typeof BundleService>,
  };
}

describe('operation pages', () => {
  beforeEach(() => {
    sessionStorage.clear();
    setActivePinia(createPinia());
  });

  it('shows the correct fields for INSTALL, UPGRADE, and ROLLBACK', async () => {
    const { wrapper } = await mountCreatePage(optionsClients());

    expect(wrapper.text()).toContain('制品 Bundle');
    expect(wrapper.text()).not.toContain('当前 Revision');
    expect(wrapper.text()).not.toContain('回退目标 Operation');

    await wrapper.find('input[value="UPGRADE"]').setValue(true);
    expect(wrapper.text()).toContain('当前 Revision');

    await wrapper.find('input[value="ROLLBACK"]').setValue(true);
    expect(wrapper.text()).toContain('制品 Bundle');
    expect(wrapper.text()).toContain('当前 Revision');
    expect(wrapper.text()).not.toContain('Patch 覆盖');
    expect(wrapper.text()).not.toContain('回退目标 Operation');
  });

  it('marks a non-bundle image patch on the invalid row', async () => {
    const { wrapper } = await mountCreatePage(optionsClients());
    await fillBundleAndValues(wrapper);
    await wrapper.findAll('button').find((button) => button.text() === '添加 Patch')?.trigger('click');
    await wrapper.find('input[aria-label="Patch 1 path"]').setValue('sidecar.image.tag');
    await wrapper.find('input[aria-label="Patch 1 value"]').setValue('v2');

    await wrapper.find('form').trigger('submit');

    const invalidRow = wrapper.find('.patch-editor__row--error');
    expect(invalidRow.exists()).toBe(true);
    expect(invalidRow.text()).toContain('Patch 引用了 Bundle 外镜像');
    expect(invalidRow.find('input[aria-label="Patch 1 path"]').attributes('aria-invalid')).toBe('true');
  });

  it('opens confirmation and cancel preserves the form without creating', async () => {
    const clients = optionsClients();
    const { wrapper } = await mountCreatePage(clients);
    await fillBundleAndValues(wrapper);
    await wrapper.find('form').trigger('submit');

    expect(wrapper.text()).toContain('最终确认');
    await wrapper.findAll('button').find((button) => button.text() === '取消')?.trigger('click');

    expect(wrapper.text()).toContain('创建发布操作');
    expect(clients.operations.createOperation).not.toHaveBeenCalled();
  });

  it('shows a guided empty state when required options are unavailable', async () => {
    setOperationClientForTest(emptyOptionsClients().operations, emptyOptionsClients().bundles);
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/operations/new', name: 'OperationCreate', component: OperationCreatePage },
        { path: '/customers/:customerId/clusters/:clusterId/releases', name: 'ReleaseInventory', component: { template: '<div />' } },
      ],
    });
    await router.push('/customers/cust-1/clusters/cluster-1/releases/def-1/operations/new');
    await router.isReady();
    const wrapper = mount(OperationCreatePage, { global: { plugins: [createPinia(), router] } });

    await vi.waitFor(() => expect(wrapper.text()).toContain('没有可创建操作的选项'));
    expect(wrapper.text()).toContain('validated Bundle');
  });

  it('locks confirmation and links to the active operation on release_busy', async () => {
    let resolveCreate: ((value: unknown) => void) | undefined;
    const createPromise = new Promise((resolve) => { resolveCreate = resolve; });
    const clients = optionsClients();
    clients.operations.createOperation = vi.fn()
      .mockReturnValueOnce(createPromise)
      .mockRejectedValueOnce(new ConnectError('release busy', Code.FailedPrecondition, {
        'X-Reason-Code': 'release_busy',
        'X-Operation-ID': 'op-active',
      }));
    const { wrapper, router } = await mountCreatePage(clients);
    await fillBundleAndValues(wrapper);
    await wrapper.find('form').trigger('submit');

    const confirm = wrapper.findAll('button').find((button) => button.text() === '确认创建');
    await confirm?.trigger('click');
    expect(confirm?.attributes('disabled')).toBeDefined();
    await confirm?.trigger('click');
    expect(clients.operations.createOperation).toHaveBeenCalledTimes(1);
    resolveCreate?.(create(CreateOperationResponseSchema, {
      operationId: 'op-created', state: 'preflight', preflightId: 'pf-created',
    }));
    await vi.waitFor(() => expect(router.currentRoute.value.params.operationId).toBe('op-created'));

    const second = await mountCreatePage(clients);
    await fillBundleAndValues(second.wrapper);
    await second.wrapper.find('form').trigger('submit');
    await second.wrapper.findAll('button').find((button) => button.text() === '确认创建')?.trigger('click');
    await vi.waitFor(() => expect(second.wrapper.text()).toContain('查看进行中操作'));
    await second.wrapper.find('.operation-page__existing').trigger('click');
    await vi.waitFor(() => expect(second.router.currentRoute.value.params.operationId).toBe('op-active'));
  });

  it('resets the operation timeline store when the operation detail page unmounts', async () => {
    const clients: TestClients = {
      operations: {
        watchOperation: vi.fn().mockResolvedValue((async function* () {
          yield create(WatchOperationResponseSchema, {
            payload: {
              case: 'snapshot',
              value: create(OperationSnapshotSchema, {
                operation: create(OperationSchema, {
                  operationId: 'op-active',
                  operationType: 'INSTALL',
                  state: OperationStatus.PREFLIGHT,
                }),
                snapshotSequence: 1n,
                retainedFromSequence: 1n,
              }),
            },
          });
          await new Promise<void>(() => undefined);
        })()),
      } as unknown as Client<typeof OrchestratorService>,
      bundles: emptyOptionsClients().bundles,
    };
    setOperationClientForTest(clients.operations, clients.bundles);
    const pinia = createPinia();
    setActivePinia(pinia);
    const timelineStore = useOperationTimelineStore(pinia);
    const reset = vi.spyOn(timelineStore, 'reset');
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/operations/:operationId', name: 'OperationDetail', component: OperationDetailPage },
        { path: '/customers/:customerId/clusters/:clusterId/releases', name: 'ReleaseInventory', component: { template: '<div />' } },
      ],
    });
    await router.push('/customers/cust-1/clusters/cluster-1/releases/def-1/operations/op-active');
    await router.isReady();
    const wrapper = mount(OperationDetailPage, { global: { plugins: [pinia, router] } });
    await vi.waitFor(() => expect(timelineStore.operation?.operationId).toBe('op-active'));

    wrapper.unmount();

    expect(reset).toHaveBeenCalled();
    expect(timelineStore.operation).toBeNull();
  });

  function detailRoute(router: Router): void {
    router.addRoute({
      path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/operations/:operationId',
      name: 'OperationDetail',
      component: OperationDetailPage,
    });
    router.addRoute({
      path: '/customers/:customerId/clusters/:clusterId/releases',
      name: 'ReleaseInventory',
      component: { template: '<div />' },
    });
  }

  it('AC-13: renders a disabled 取消中… button while the operation is cancelling', async () => {
    const clients: TestClients = {
      operations: {
        watchOperation: vi.fn().mockResolvedValue((async function* () {
          yield create(WatchOperationResponseSchema, {
            payload: {
              case: 'snapshot',
              value: create(OperationSnapshotSchema, {
                operation: create(OperationSchema, {
                  operationId: 'op-cancelling',
                  operationType: 'INSTALL',
                  state: OperationStatus.CANCELLING,
                }),
                snapshotSequence: 1n,
                retainedFromSequence: 1n,
              }),
            },
          });
          await new Promise<void>(() => undefined);
        })()),
      } as unknown as Client<typeof OrchestratorService>,
      bundles: emptyOptionsClients().bundles,
    };
    setOperationClientForTest(clients.operations, clients.bundles);
    const pinia = createPinia();
    setActivePinia(pinia);
    const router = createRouter({ history: createMemoryHistory(), routes: [] });
    detailRoute(router);
    await router.push('/customers/cust-1/clusters/cluster-1/releases/def-1/operations/op-cancelling');
    await router.isReady();
    const wrapper = mount(OperationDetailPage, { global: { plugins: [pinia, router] } });

    await vi.waitFor(() => expect(wrapper.text()).toContain('取消中…'));
    const button = wrapper.findAll('button').find((b) => b.text() === '取消中…');
    expect(button?.attributes('disabled')).toBeDefined();
  });

  it('AC-15: route scope change with the same operationId reloads the store', async () => {
    let watchCalls = 0;
    const clients: TestClients = {
      operations: {
        watchOperation: vi.fn().mockImplementation(() => {
          watchCalls++;
          return (async function* () {
            yield create(WatchOperationResponseSchema, {
              payload: {
                case: 'snapshot',
                value: create(OperationSnapshotSchema, {
                  operation: create(OperationSchema, {
                    operationId: 'op-shared',
                    operationType: 'INSTALL',
                    state: OperationStatus.PREFLIGHT,
                  }),
                  snapshotSequence: 1n,
                  retainedFromSequence: 1n,
                }),
              },
            });
            await new Promise<void>(() => undefined);
          })();
        }),
      } as unknown as Client<typeof OrchestratorService>,
      bundles: emptyOptionsClients().bundles,
    };
    setOperationClientForTest(clients.operations, clients.bundles);
    const pinia = createPinia();
    setActivePinia(pinia);
    const timelineStore = useOperationTimelineStore(pinia);
    const router = createRouter({ history: createMemoryHistory(), routes: [] });
    detailRoute(router);
    await router.push('/customers/cust-1/clusters/cluster-1/releases/def-1/operations/op-shared');
    await router.isReady();
    const wrapper = mount(OperationDetailPage, { global: { plugins: [pinia, router] } });
    await vi.waitFor(() => expect(timelineStore.operation?.operationId).toBe('op-shared'));
    expect(watchCalls).toBe(1);

    // Same operationId under a different cluster/release: the store must
    // reset and open a fresh stream (AC-057-15).
    await router.push('/customers/cust-1/clusters/cluster-2/releases/def-2/operations/op-shared');
    await vi.waitFor(() => expect(watchCalls).toBe(2));
    await vi.waitFor(() => expect(timelineStore.operation?.operationId).toBe('op-shared'));
    wrapper.unmount();
  });

  it('AC-07: a viewer sees no cancel affordance even for terminal operations', async () => {
    const clients: TestClients = {
      operations: {
        watchOperation: vi.fn().mockResolvedValue((async function* () {
          yield create(WatchOperationResponseSchema, {
            payload: {
              case: 'snapshot',
              value: create(OperationSnapshotSchema, {
                operation: create(OperationSchema, {
                  operationId: 'op-viewer',
                  operationType: 'INSTALL',
                  state: OperationStatus.SUCCEEDED,
                }),
                snapshotSequence: 1n,
                retainedFromSequence: 1n,
              }),
            },
          });
          await new Promise<void>(() => undefined);
        })()),
      } as unknown as Client<typeof OrchestratorService>,
      bundles: emptyOptionsClients().bundles,
    };
    setOperationClientForTest(clients.operations, clients.bundles);
    const pinia = createPinia();
    setActivePinia(pinia);
    const timelineStore = useOperationTimelineStore(pinia);
    timelineStore.configure({ canWrite: () => false });
    const router = createRouter({ history: createMemoryHistory(), routes: [] });
    detailRoute(router);
    await router.push('/customers/cust-1/clusters/cluster-1/releases/def-1/operations/op-viewer');
    await router.isReady();
    const wrapper = mount(OperationDetailPage, { global: { plugins: [pinia, router] } });
    await vi.waitFor(() => expect(wrapper.text()).toContain('succeeded'));

    expect(wrapper.text()).not.toContain('取消操作');
    expect(wrapper.text()).not.toContain('取消中');
  });

  it('AC-16: the manual refresh button re-fetches via GetOperation', async () => {
    const getOperation = vi.fn().mockResolvedValue(create(GetOperationResponseSchema, {
      operation: create(OperationSchema, {
        operationId: 'op-refresh',
        operationType: 'INSTALL',
        state: OperationStatus.RUNNING,
        stateVersion: 2n,
      }),
    }));
    const clients: TestClients = {
      operations: {
        watchOperation: vi.fn().mockResolvedValue((async function* () {
          yield create(WatchOperationResponseSchema, {
            payload: {
              case: 'snapshot',
              value: create(OperationSnapshotSchema, {
                operation: create(OperationSchema, {
                  operationId: 'op-refresh',
                  operationType: 'INSTALL',
                  state: OperationStatus.RUNNING,
                  stateVersion: 1n,
                }),
                snapshotSequence: 1n,
                retainedFromSequence: 1n,
              }),
            },
          });
          await new Promise<void>(() => undefined);
        })()),
        getOperation,
      } as unknown as Client<typeof OrchestratorService>,
      bundles: emptyOptionsClients().bundles,
    };
    setOperationClientForTest(clients.operations, clients.bundles);
    const pinia = createPinia();
    setActivePinia(pinia);
    const timelineStore = useOperationTimelineStore(pinia);
    const router = createRouter({ history: createMemoryHistory(), routes: [] });
    detailRoute(router);
    await router.push('/customers/cust-1/clusters/cluster-1/releases/def-1/operations/op-refresh');
    await router.isReady();
    const wrapper = mount(OperationDetailPage, { global: { plugins: [pinia, router] } });
    await vi.waitFor(() => expect(timelineStore.operation?.stateVersion).toBe(1n));

    await wrapper.findAll('button').find((b) => b.text() === '刷新')?.trigger('click');
    await vi.waitFor(() => expect(getOperation).toHaveBeenCalled());
    await vi.waitFor(() => expect(timelineStore.operation?.stateVersion).toBe(2n));
  });

  it('AC-02: a terminal operation disables the cancel button with the completion tooltip', async () => {
    const clients: TestClients = {
      operations: {
        watchOperation: vi.fn().mockResolvedValue((async function* () {
          yield create(WatchOperationResponseSchema, {
            payload: {
              case: 'snapshot',
              value: create(OperationSnapshotSchema, {
                operation: create(OperationSchema, {
                  operationId: 'op-terminal',
                  operationType: 'INSTALL',
                  state: OperationStatus.SUCCEEDED,
                }),
                snapshotSequence: 1n,
                retainedFromSequence: 1n,
              }),
            },
          });
          await new Promise<void>(() => undefined);
        })()),
      } as unknown as Client<typeof OrchestratorService>,
      bundles: emptyOptionsClients().bundles,
    };
    setOperationClientForTest(clients.operations, clients.bundles);
    const pinia = createPinia();
    setActivePinia(pinia);
    const router = createRouter({ history: createMemoryHistory(), routes: [] });
    detailRoute(router);
    await router.push('/customers/cust-1/clusters/cluster-1/releases/def-1/operations/op-terminal');
    await router.isReady();
    const wrapper = mount(OperationDetailPage, { global: { plugins: [pinia, router] } });
    await vi.waitFor(() => expect(wrapper.text()).toContain('succeeded'));

    const button = wrapper.findAll('button').find((b) => b.text() === '取消操作');
    expect(button?.attributes('disabled')).toBeDefined();
    expect(button?.attributes('title')).toBe('操作已完成，无法取消');
  });

  it('AC-27: the cancel entry stays usable while the stream is disconnected', async () => {
    const cancelOperation = vi.fn().mockResolvedValue(create(CancelOperationResponseSchema, {
      operation: create(OperationSchema, {
        operationId: 'op-degraded',
        operationType: 'INSTALL',
        state: OperationStatus.CANCELLING,
        stateVersion: 4n,
      }),
      requestId: 'req-degraded',
    }));
    const clients: TestClients = {
      operations: {
        // Deliberately finite stream: the store must fall back to degraded
        // mode (disconnected) instead of staying connected (AC-057-05).
        watchOperation: vi.fn().mockResolvedValue((async function* () {
          yield create(WatchOperationResponseSchema, {
            payload: {
              case: 'snapshot',
              value: create(OperationSnapshotSchema, {
                operation: create(OperationSchema, {
                  operationId: 'op-degraded',
                  operationType: 'INSTALL',
                  state: OperationStatus.RUNNING,
                  stateVersion: 3n,
                }),
                snapshotSequence: 1n,
                retainedFromSequence: 1n,
              }),
            },
          });
        })()),
        cancelOperation,
      } as unknown as Client<typeof OrchestratorService>,
      bundles: emptyOptionsClients().bundles,
    };
    setOperationClientForTest(clients.operations, clients.bundles);
    const pinia = createPinia();
    setActivePinia(pinia);
    const timelineStore = useOperationTimelineStore(pinia);
    const router = createRouter({ history: createMemoryHistory(), routes: [] });
    detailRoute(router);
    await router.push('/customers/cust-1/clusters/cluster-1/releases/def-1/operations/op-degraded');
    await router.isReady();
    const wrapper = mount(OperationDetailPage, { global: { plugins: [pinia, router] } });
    await vi.waitFor(() => expect(timelineStore.streamStatus).toBe('disconnected'));

    // Banner shown, data retained, cancel entry still rendered and enabled.
    expect(wrapper.text()).toContain('实时更新已断开，正在重连…');
    expect(timelineStore.operation?.stateVersion).toBe(3n);
    const button = wrapper.findAll('button').find((b) => b.text() === '取消操作');
    expect(button).toBeDefined();
    expect(button?.attributes('disabled')).toBeUndefined();

    // A real cancel submit while disconnected must use the last
    // authoritative state_version (AC-057-27).
    await button?.trigger('click');
    await wrapper.find('textarea').setValue('断线取消');
    await wrapper.findAll('button').find((b) => b.text() === '确认取消')?.trigger('click');
    await vi.waitFor(() => expect(cancelOperation).toHaveBeenCalledTimes(1));
    const [request] = cancelOperation.mock.calls[0] as [never, never];
    expect(request).toEqual(expect.objectContaining({
      operationId: 'op-degraded',
      reason: '断线取消',
      expectedStateVersion: 3n,
    }));
    expect(timelineStore.operation?.state).toBe('cancelling');
    wrapper.unmount();
  });
});
