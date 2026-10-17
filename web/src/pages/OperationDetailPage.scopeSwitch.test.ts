import { create } from '@bufbuild/protobuf';
import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { createMemoryHistory, createRouter } from 'vue-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Client } from '@connectrpc/connect';
import {
  GetOperationResponseSchema,
  OperationSchema,
  OperationSnapshotSchema,
  OperationStatus,
  OrchestratorService,
  WatchOperationResponseSchema,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { getDefinition, type DefinitionView } from '@/connect/definition-api';
import { setEmergencyClientForTest, type EmergencyAuthorizationProjection } from '@/connect/emergency-api';
import { setOperationClientForTest } from '@/connect/operation-api';
import { useAuthStore } from '@/stores/auth';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import { useOperationTimelineStore } from '@/stores/operationTimeline';
import OperationDetailPage from './OperationDetailPage.vue';

/*
 * TASK-286: changing the release scope inside ONE detail-page instance (the canonical
 * scoped EMERGENCY detail -> the scope-less Operation-centre detail) used to leave the
 * page stuck on "正在加载 Operation…" forever. The scope switch reset()s the timeline and
 * opens a fresh stream; the EMERGENCY effect observation then observed isEmergency go
 * false (the reset nulled the operation) and called stop() -> timeline.reset(), which
 * bumped the scope generation and discarded the stream that had just started. The
 * effect observation is torn down as part of the scope switch itself, before the new
 * load starts, so it can no longer invalidate the fresh stream.
 *
 * The mock is the same TASK-279 definition read the page uses to recover the release
 * context on the scope-less route.
 */
vi.mock('@/connect/definition-api', async (importOriginal) => {
  const original = await importOriginal<typeof import('@/connect/definition-api')>();
  return { ...original, getDefinition: vi.fn() };
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
    sourceVersion: 7n,
    policyVersion: 1n,
    checkpoint: 7n,
    fresh: true,
    actorId: 'user-1',
    emergencyChangeEnabled: true,
  };
}

/** A live EMERGENCY operation: one snapshot, then the stream stays open. */
function operationsClient(): Client<typeof OrchestratorService> {
  const operation = () =>
    create(OperationSchema, {
      operationId: 'op-1',
      operationType: 'EMERGENCY',
      state: OperationStatus.RUNNING,
      releaseDefinitionId: 'def-1',
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

function appRouter() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/customers/:customerId/clusters/:clusterId/releases', name: 'ReleaseInventory', component: { template: '<div />' } },
      { path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/operations', name: 'OperationList', component: { template: '<div />' } },
      { path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/operations/:operationId', name: 'OperationDetail', component: OperationDetailPage },
      { path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/emergency/convergence', name: 'ConvergenceTasks', component: { template: '<div />' } },
      { path: '/operations/:operationId', name: 'OperationCenterDetail', component: OperationDetailPage },
      { path: '/operations', name: 'OperationCenter', component: { template: '<div />' } },
    ],
  });
  return router;
}

beforeEach(() => {
  setActivePinia(createPinia());
  vi.mocked(getDefinition).mockReset();
  useEmergencyAuthorizationStore().configure({ loadSnapshot: async () => snapshot() });
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

describe('OperationDetailPage scope switch (TASK-286)', () => {
  it('renders the operation after switching from a scoped EMERGENCY detail to the scope-less route', async () => {
    vi.mocked(getDefinition).mockResolvedValue({
      customerId: 'cust-1',
      clusterId: 'cluster-1',
    } as unknown as DefinitionView);
    const client = operationsClient();
    setOperationClientForTest(client);
    setEmergencyClientForTest(client);

    const router = appRouter();
    await router.push('/customers/cust-1/clusters/cluster-1/releases/def-1/operations/op-1?releaseName=checkout');
    await router.isReady();
    const wrapper = mount({ template: '<RouterView />' }, { global: { plugins: [router] } });
    const timeline = useOperationTimelineStore();

    // The canonical scoped EMERGENCY detail renders the operation.
    await vi.waitFor(() => expect(timeline.operation?.operationId).toBe('op-1'));
    expect(wrapper.text()).not.toContain('正在加载 Operation…');
    expect(wrapper.text()).toContain('def-1');

    // Same route component (`RouterView` keeps the instance); remember the element.
    const instanceElement = wrapper.find('.operation-detail').element;

    // Switch to the scope-less detail inside that same instance.
    await router.push('/operations/op-1?releaseName=checkout');
    expect(router.currentRoute.value.name).toBe('OperationCenterDetail');
    await flushPromises();

    // The page must converge on rendered content, not stay on the loading state.
    await vi.waitFor(() => expect(wrapper.text()).not.toContain('正在加载 Operation…'));
    expect(wrapper.find('.operation-detail').element).toBe(instanceElement);
    expect(wrapper.text()).toContain('op-1');
    expect(timeline.operation?.operationId).toBe('op-1');
  });

  it('drops a definition read that resolves after the store moved to another definition', async () => {
    let resolveStaleDefinition!: (value: DefinitionView) => void;
    vi.mocked(getDefinition).mockImplementation((definitionId: string) => {
      if (definitionId === 'def-1') {
        return new Promise<DefinitionView>((resolve) => {
          resolveStaleDefinition = resolve;
        });
      }
      return Promise.resolve({ customerId: 'cust-2', clusterId: 'cluster-2' } as unknown as DefinitionView);
    });

    let releaseSecondSnapshot!: () => void;
    const secondSnapshotGate = new Promise<void>((resolve) => {
      releaseSecondSnapshot = resolve;
    });
    const operationFor = (releaseDefinitionId: string) =>
      create(OperationSchema, {
        operationId: 'op-1',
        operationType: 'EMERGENCY',
        state: OperationStatus.RUNNING,
        releaseDefinitionId,
      });
    const client = {
      watchOperation: vi.fn(async () =>
        (async function* () {
          yield create(WatchOperationResponseSchema, {
            payload: {
              case: 'snapshot',
              value: create(OperationSnapshotSchema, {
                operation: operationFor('def-1'),
                snapshotSequence: 1n,
                retainedFromSequence: 1n,
              }),
            },
          });
          await secondSnapshotGate;
          yield create(WatchOperationResponseSchema, {
            payload: {
              case: 'snapshot',
              value: create(OperationSnapshotSchema, {
                operation: operationFor('def-2'),
                snapshotSequence: 2n,
                retainedFromSequence: 1n,
              }),
            },
          });
          await new Promise<void>(() => undefined);
        })(),
      ),
      getOperation: vi.fn(async () => create(GetOperationResponseSchema, { operation: operationFor('def-2') })),
    } as unknown as Client<typeof OrchestratorService>;
    setOperationClientForTest(client);
    setEmergencyClientForTest(client);

    const router = appRouter();
    await router.push('/operations/op-1?releaseName=checkout');
    await router.isReady();
    const wrapper = mount(OperationDetailPage, { global: { plugins: [router] } });

    // The first operation (def-1) is on screen and its definition read is in flight.
    await vi.waitFor(() => expect(getDefinition).toHaveBeenCalledWith('def-1'));

    // The store moves on to a different definition under the SAME route param.
    releaseSecondSnapshot();
    await vi.waitFor(() => expect(getDefinition).toHaveBeenCalledWith('def-2'));
    const scopedCrumbs = () =>
      wrapper.get('.operation-detail__breadcrumbs').html();
    await vi.waitFor(() => expect(scopedCrumbs()).toContain('/customers/cust-2/clusters/cluster-2/releases'));

    // The def-1 read now resolves with a stale scope: the route param is still
    // op-1, so only the store's identity can tell that it no longer applies.
    resolveStaleDefinition({ customerId: 'cust-9', clusterId: 'cluster-9' } as unknown as DefinitionView);
    await flushPromises();

    expect(scopedCrumbs()).toContain('/customers/cust-2/clusters/cluster-2/releases');
    expect(scopedCrumbs()).not.toContain('cust-9');
  });
});
