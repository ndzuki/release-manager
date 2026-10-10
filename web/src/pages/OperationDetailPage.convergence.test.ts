import { create } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { createMemoryHistory } from 'vue-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Client } from '@connectrpc/connect';
import {
  EmergencyAction,
  EmergencyConvergence,
  EmergencyEffectStatus,
  EmergencyResultSchema,
  GetOperationResponseSchema,
  ListNonTerminalOperationsResponseSchema,
  NonTerminalOperationSummarySchema,
  OperationSchema,
  OperationSnapshotSchema,
  OperationStatus,
  OrchestratorService,
  WatchOperationResponseSchema,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { getDefinition, type DefinitionView } from '@/connect/definition-api';
import { setEmergencyClientForTest, type EmergencyAuthorizationProjection } from '@/connect/emergency-api';
import { setOperationClientForTest } from '@/connect/operation-api';
import { createAppRouter } from '@/router';
import { useAuthStore } from '@/stores/auth';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import OperationDetailPage from './OperationDetailPage.vue';

/*
 * TASK-279 AC-279-02: the aggregate row now carries the cluster, so the detail page
 * reached from the Operation centre has the real release context and must offer the
 * release-scoped actions the scope-less route used to mask. The convergence entry is
 * the observable one: it needs customer + cluster + release to build its route.
 *
 * The definition read is mocked only for the scope-less half (a URL without
 * customer/cluster/release); the centre-entered half must not need it at all, which
 * the first case asserts by entering through the real centre link.
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

/** One EMERGENCY operation whose effect is APPLIED and awaiting convergence. */
function operationsClient(): Client<typeof OrchestratorService> {
  const operation = () =>
    create(OperationSchema, {
      operationId: 'op-1',
      operationType: 'EMERGENCY',
      state: OperationStatus.RUNNING,
      releaseDefinitionId: 'def-1',
    });
  const emergencyResult = () =>
    create(EmergencyResultSchema, {
      opType: EmergencyAction.SET_REPLICAS,
      convergencePolicy: EmergencyConvergence.REQUIRE_PROMOTION,
      effectStatus: EmergencyEffectStatus.APPLIED,
      requested: true,
    });
  return {
    listNonTerminalOperations: vi.fn(async () =>
      create(ListNonTerminalOperationsResponseSchema, {
        operations: [
          create(NonTerminalOperationSummarySchema, {
            operationId: 'op-1',
            operationType: 'EMERGENCY',
            state: 'running',
            releaseDefinitionId: 'def-1',
            releaseDefinitionName: 'checkout',
            customerId: 'cust-1',
            customerName: 'Acme',
            clusterId: 'cluster-1',
            createdAt: timestampFromDate(new Date('2026-10-01T00:00:00Z')),
            updatedAt: timestampFromDate(new Date('2026-10-01T00:05:00Z')),
            revision: 7,
            emergency: true,
          }),
        ],
      }),
    ),
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
    getOperation: vi.fn(async () =>
      create(GetOperationResponseSchema, { operation: operation(), emergencyResult: emergencyResult() }),
    ),
  } as unknown as Client<typeof OrchestratorService>;
}

/** The convergence CTA: only rendered when the page holds a release scope. */
function convergenceButton(wrapper: VueWrapper) {
  return wrapper.findAll('button').find((button) => button.text() === '创建 ValuesRevision 收敛');
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

describe('OperationDetailPage convergence context (TASK-279)', () => {
  it('offers the convergence action on the detail page entered from the Operation centre', async () => {
    const client = operationsClient();
    setOperationClientForTest(client);
    setEmergencyClientForTest(client);

    const router = createAppRouter(createMemoryHistory(), true, true, true);
    await router.push('/operations');
    await router.isReady();
    const wrapper = mount({ template: '<RouterView />' }, { global: { plugins: [router] } });
    await flushPromises();

    // The centre links into the canonical release-scoped route, built from the
    // cluster the aggregate row carries — no definition read is involved.
    const link = wrapper.get('tbody tr a');
    expect(link.attributes('href')).toBe(
      '/customers/cust-1/clusters/cluster-1/releases/def-1/operations/op-1?releaseName=checkout',
    );

    await link.trigger('click');
    await flushPromises();

    expect(router.currentRoute.value.name).toBe('OperationDetail');
    await vi.waitFor(() => expect(convergenceButton(wrapper)).toBeDefined());
    expect(convergenceButton(wrapper)!.attributes('disabled')).toBeUndefined();
    expect(getDefinition).not.toHaveBeenCalled();
  });

  it('recovers the release context from the definition on the scope-less route', async () => {
    vi.mocked(getDefinition).mockResolvedValue({
      customerId: 'cust-1',
      clusterId: 'cluster-1',
    } as unknown as DefinitionView);
    const client = operationsClient();
    setOperationClientForTest(client);
    setEmergencyClientForTest(client);

    const router = createAppRouter(createMemoryHistory(), true, true, true);
    await router.push('/operations/op-1?releaseName=checkout');
    await router.isReady();
    const wrapper = mount(OperationDetailPage, { global: { plugins: [router] } });

    // The breadcrumbs point at the real cluster the definition reports, and the
    // action is available — this is the mask TASK-279 removes.
    await vi.waitFor(() =>
      expect(wrapper.find('.operation-detail__breadcrumbs a[href^="/customers/"]').exists()).toBe(true),
    );
    expect(getDefinition).toHaveBeenCalledWith('def-1');
    expect(wrapper.get('.operation-detail__breadcrumbs').html()).toContain(
      '/customers/cust-1/clusters/cluster-1/releases',
    );

    await vi.waitFor(() => expect(convergenceButton(wrapper)).toBeDefined());
    await convergenceButton(wrapper)!.trigger('click');
    await vi.waitFor(() => expect(router.currentRoute.value.name).toBe('ConvergenceTasks'));
    expect(router.currentRoute.value.params).toMatchObject({
      customerId: 'cust-1',
      clusterId: 'cluster-1',
      releaseId: 'def-1',
    });
  });

  it('keeps the scope-less rendering when the definition read is refused', async () => {
    vi.mocked(getDefinition).mockRejectedValue(new Error('release:read denied'));
    const client = operationsClient();
    setOperationClientForTest(client);
    setEmergencyClientForTest(client);

    const router = createAppRouter(createMemoryHistory(), true, true, true);
    await router.push('/operations/op-1?releaseName=checkout');
    await router.isReady();
    const wrapper = mount(OperationDetailPage, { global: { plugins: [router] } });

    // The operation and its panel still render; only the scoped navigation stays
    // closed, because there is genuinely no context to build it from.
    await vi.waitFor(() => expect(wrapper.text()).toContain('紧急变更结果'));
    expect(wrapper.get('.operation-detail__breadcrumbs').text()).not.toContain('Releases');
    expect(convergenceButton(wrapper)).toBeUndefined();
  });
});
