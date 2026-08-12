import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import OperatorDetailPage from './OperatorDetailPage.vue';
import OperatorEnrollPage from './OperatorEnrollPage.vue';
import OperatorListPage from './OperatorListPage.vue';
import { getEnrollmentTokenStatus, getOperator, listOperators } from '@/connect/operator-api';
import type * as OperatorApi from '@/connect/operator-api';
import { useAuthStore } from '@/stores/auth';
vi.mock('@/connect/operator-api', async (importOriginal) => {
  const original = await importOriginal<typeof OperatorApi>();
  return {
    ...original,
    getEnrollmentTokenStatus: vi.fn(),
    getOperator: vi.fn(),
    listOperators: vi.fn(),
  };
});

function authenticate(role: string): void {
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: {
      $typeName: 'auth.v1.SessionUser',
      id: 'user-1',
      username: role,
      roles: [role],
      activeOrgId: 'org-1',
    },
  });
}

beforeEach(() => {
  setActivePinia(createPinia());
  vi.mocked(listOperators).mockReset();
  vi.mocked(getOperator).mockReset();
  vi.mocked(getEnrollmentTokenStatus).mockReset();
});

describe('Operator pages', () => {
  it('shows the empty-state guide without write actions for a viewer', async () => {
    authenticate('viewer');
    vi.mocked(listOperators).mockResolvedValue({
      operators: [],
      nextPageToken: null,
      totalCount: 0,
      heartbeatIntervalSeconds: 15,
    });
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/customers/:customerId/clusters/:clusterId/operators', name: 'OperatorList', component: OperatorListPage },
        { path: '/customers/:customerId/clusters/:clusterId/operators/new', name: 'OperatorEnroll', component: { template: '<div />' } },
      ],
    });
    await router.push('/customers/customer-1/clusters/cluster-1/operators');
    await router.isReady();

    const wrapper = mount(OperatorListPage, { global: { plugins: [router] } });
    await flushPromises();

    expect(wrapper.text()).toContain('No operators registered');
    expect(wrapper.text()).not.toContain('Generate token');
    expect(wrapper.text()).not.toContain('Generate the first token');
    expect(wrapper.text()).not.toContain('Revoke');
  });

  it('renders the server-owned offline reason and heartbeat without deriving status', async () => {
    authenticate('viewer');
    vi.mocked(getOperator).mockResolvedValue({
      operator: {
        id: 'operator-1',
        name: 'operator-one',
        customerId: 'customer-1',
        clusterId: 'cluster-1',
        clusterName: 'Staging',
        lifecycleStatus: 'active',
        sessionStatus: 'offline',
        sessionStatusReason: 'heartbeat_timeout',
        lastHeartbeat: '2026-07-20T01:02:03.000Z',
        registeredAt: '2026-07-01T00:00:00.000Z',
        supersededAt: null,
        revokedAt: null,
        supersededBy: null,
        revokeReason: null,
        instanceId: null,
        version: null,
        capabilities: {},
      },
      heartbeatIntervalSeconds: 15,
    });
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/customers/:customerId/clusters/:clusterId/operators', name: 'OperatorList', component: { template: '<div />' } },
        { path: '/customers/:customerId/clusters/:clusterId/operators/:operatorId', name: 'OperatorDetail', component: OperatorDetailPage },
      ],
    });
    await router.push('/customers/customer-1/clusters/cluster-1/operators/operator-1');
    await router.isReady();

    const wrapper = mount(OperatorDetailPage, { global: { plugins: [router] } });
    await flushPromises();

    expect(wrapper.text().toLowerCase()).toContain('offline');
    expect(wrapper.text()).toContain('Heartbeat timed out.');
    expect(wrapper.text()).not.toContain('Revoke operator');
  });

  it('hides enrollment, replacement, and pending-token revocation controls from viewers', async () => {
    authenticate('viewer');
    vi.mocked(getEnrollmentTokenStatus).mockResolvedValue({
      state: 'pending',
      createdAt: '2026-07-27T01:00:00.000Z',
      expiresAt: '2026-07-27T02:00:00.000Z',
      createdByDisplayName: 'release-admin',
    });
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        { path: '/customers/:customerId/clusters/:clusterId/operators', name: 'OperatorList', component: { template: '<div />' } },
        { path: '/customers/:customerId/clusters/:clusterId/operators/new', name: 'OperatorEnroll', component: OperatorEnrollPage },
      ],
    });
    await router.push('/customers/customer-1/clusters/cluster-1/operators/new');
    await router.isReady();

    const wrapper = mount(OperatorEnrollPage, { global: { plugins: [router] } });
    await flushPromises();

    expect(wrapper.text()).toContain('Access denied');
    expect(wrapper.text()).not.toContain('Replace token');
    expect(wrapper.text()).not.toContain('Revoke pending token');
    expect(wrapper.text()).not.toContain('Generate enrollment token');
  });
});
