import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import OperatorDetailPage from './OperatorDetailPage.vue';
import OperatorEnrollPage from './OperatorEnrollPage.vue';
import OperatorListPage from './OperatorListPage.vue';
import {
  createEnrollmentToken,
  getEnrollmentTokenStatus,
  getOperator,
  listOperators,
  revokePendingEnrollmentToken,
} from '@/connect/operator-api';
import type * as OperatorApi from '@/connect/operator-api';
import { useAuthStore } from '@/stores/auth';
vi.mock('@/connect/operator-api', async (importOriginal) => {
  const original = await importOriginal<typeof OperatorApi>();
  return {
    ...original,
    getEnrollmentTokenStatus: vi.fn(),
    getOperator: vi.fn(),
    listOperators: vi.fn(),
    createEnrollmentToken: vi.fn(),
    revokePendingEnrollmentToken: vi.fn(),
  };
});

function dialogPanel(): HTMLElement {
  const panel = document.querySelector<HTMLElement>('.app-dialog__panel');
  if (!panel) throw new Error('confirmation dialog not rendered');
  return panel;
}

function clickDialogButton(root: HTMLElement, label: string): void {
  const button = Array.from(root.querySelectorAll('button')).find((element) => element.textContent?.trim() === label);
  if (!button) throw new Error(`dialog button not found: ${label}`);
  button.click();
}

async function mountEnrollPage() {
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
  return wrapper;
}

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
  vi.mocked(createEnrollmentToken).mockReset().mockResolvedValue({
    token: 'token-plaintext',
    expiresAt: '2026-10-01T01:00:00.000Z',
    customerId: 'customer-1',
    clusterId: 'cluster-1',
    clusterName: 'Staging',
    operatorEndpoint: 'operator.example:443',
    installCommandTemplateVersion: 'v1',
    installCommandTemplate: 'helm install ${ENROLLMENT_TOKEN}',
  });
  vi.mocked(revokePendingEnrollmentToken).mockReset().mockResolvedValue(true);
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

    expect(wrapper.text()).toContain('暂无已注册的 Operator');
    expect(wrapper.text()).not.toContain('生成令牌');
    expect(wrapper.text()).not.toContain('生成第一个令牌');
    expect(wrapper.text()).not.toContain('撤销 Operator');
    expect(wrapper.text()).not.toContain('确认撤销');
    expect(wrapper.text()).not.toContain('撤销待用令牌');
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

    expect(wrapper.text()).toContain('离线');
    expect(wrapper.text()).toContain('心跳超时。');
    expect(wrapper.text()).not.toContain('撤销 Operator');
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

    expect(wrapper.text()).toContain('无权访问');
    expect(wrapper.text()).not.toContain('替换令牌');
    expect(wrapper.text()).not.toContain('撤销待用令牌');
    expect(wrapper.text()).not.toContain('生成注册令牌');
  });

  // TASK-281: both pending-token confirmations moved from `window.confirm` to the
  // shared AppDialog. Cancel must not run the original path; confirm must.
  it('replaces a pending token only after the dialog is confirmed', async () => {
    authenticate('release_admin');
    vi.mocked(getEnrollmentTokenStatus).mockResolvedValue({
      state: 'pending',
      createdAt: '2026-07-27T01:00:00.000Z',
      expiresAt: '2026-07-27T02:00:00.000Z',
      createdByDisplayName: 'release-admin',
    });
    const wrapper = await mountEnrollPage();
    // The generation path validates the form first, so make the name valid.
    await wrapper.find('input[type="text"]').setValue('operator-one');

    await wrapper.findAll('button').find((button) => button.text() === '替换令牌')?.trigger('click');
    let panel = dialogPanel();
    expect(document.getElementById(panel.getAttribute('aria-labelledby')!)?.textContent).toBe('替换待用令牌');
    clickDialogButton(panel, '取消');
    await flushPromises();
    expect(document.querySelector('.app-dialog__panel')).toBeNull();
    expect(createEnrollmentToken).not.toHaveBeenCalled();

    await wrapper.findAll('button').find((button) => button.text() === '替换令牌')?.trigger('click');
    panel = dialogPanel();
    clickDialogButton(panel, '确认替换');
    await vi.waitFor(() => expect(createEnrollmentToken).toHaveBeenCalledTimes(1));
    expect(createEnrollmentToken).toHaveBeenCalledWith(
      'customer-1',
      'cluster-1',
      expect.objectContaining({ operatorName: 'operator-one' }),
      true,
    );
    wrapper.unmount();
  });

  it('revokes the pending token only after the dialog is confirmed', async () => {
    authenticate('release_admin');
    vi.mocked(getEnrollmentTokenStatus).mockResolvedValue({
      state: 'pending',
      createdAt: '2026-07-27T01:00:00.000Z',
      expiresAt: '2026-07-27T02:00:00.000Z',
      createdByDisplayName: 'release-admin',
    });
    const wrapper = await mountEnrollPage();

    await wrapper.findAll('button').find((button) => button.text() === '撤销待用令牌')?.trigger('click');
    let panel = dialogPanel();
    expect(document.getElementById(panel.getAttribute('aria-labelledby')!)?.textContent).toBe('撤销待用令牌');
    clickDialogButton(panel, '取消');
    await flushPromises();
    expect(document.querySelector('.app-dialog__panel')).toBeNull();
    expect(revokePendingEnrollmentToken).not.toHaveBeenCalled();

    await wrapper.findAll('button').find((button) => button.text() === '撤销待用令牌')?.trigger('click');
    panel = dialogPanel();
    clickDialogButton(panel, '确认撤销');
    await flushPromises();
    expect(revokePendingEnrollmentToken).toHaveBeenCalledWith('customer-1', 'cluster-1');
    wrapper.unmount();
  });
});
