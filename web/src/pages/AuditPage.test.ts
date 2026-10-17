import { flushPromises, mount } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import AuditPage from './AuditPage.vue';
import { auditClient } from '@/connect/client';
import { useAuthStore } from '@/stores/auth';
import { useAuditStore } from '@/stores/audit';
import type { AuditEvent, QueryAuditEventsResponse } from '@/gen/audit/v1/audit_pb';

vi.mock('@/connect/client', () => ({
  auditClient: {
    queryAuditEvents: vi.fn(),
    exportAuditEvents: vi.fn(),
  },
  authClient: { switchOrganization: vi.fn() },
  setAuthErrorHandler: vi.fn(),
}));

const queryAuditEvents = vi.mocked(auditClient.queryAuditEvents);

function auditEvent(id: string): AuditEvent {
  return {
    $typeName: 'audit.v1.AuditEvent' as const,
    id,
    actor: {
      $typeName: 'audit.v1.AuditActor' as const,
      kind: 2,
      id: `user-${id}`,
      organizationId: 'org-1',
      role: 'release_admin',
    },
    resourceType: 'operation',
    resourceId: `operation-${id}`,
    action: 'upgrade',
    status: 'succeeded',
    durationMs: 10n,
    changeSummary: '',
    metadata: {},
  };
}

function queryResponse(events: AuditEvent[], nextPageToken = ''): QueryAuditEventsResponse {
  return {
    $typeName: 'audit.v1.QueryAuditEventsResponse' as const,
    events,
    pagination: { $typeName: 'common.v1.PaginationResponse' as const, nextPageToken, totalSize: 100 },
  };
}

const ORGANIZATIONS = [
  { $typeName: 'auth.v1.Organization' as const, id: 'org-1', name: 'Acme', status: 'active', optimisticVersion: 1n },
  { $typeName: 'auth.v1.Organization' as const, id: 'org-2', name: 'Globex', status: 'active', optimisticVersion: 1n },
];

function signInAs(activeOrgId: string, roles: string[] = ['platform_admin']): void {
  const auth = useAuthStore();
  auth.status = 'authenticated';
  auth.user = {
    $typeName: 'auth.v1.SessionUser' as const,
    id: 'u1',
    username: 'admin',
    roles,
    activeOrgId,
  };
  auth.organizations = ORGANIZATIONS;
}

function mountAuditPage(): ReturnType<typeof mount> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/audit', name: 'Audit', component: AuditPage }],
  });
  return mount(AuditPage, { global: { plugins: [router] } });
}

describe('AuditPage', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    queryAuditEvents.mockReset();
  });

  it('restores filters from the URL and queries exactly once on mount (AC-059-07)', async () => {
    signInAs('org-1');
    queryAuditEvents.mockResolvedValue(queryResponse([auditEvent('a')]));
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/audit', name: 'Audit', component: AuditPage }],
    });
    await router.push('/audit?resource=operation&action=upgrade&status=succeeded');
    await router.isReady();

    const wrapper = mount(AuditPage, { global: { plugins: [router] } });
    await flushPromises();

    // URL 恢复后表单自动填充（AC-07 后半）。
    const resourceSelect = wrapper.get<HTMLSelectElement>('select[name="resource"]');
    expect(resourceSelect.element.value).toBe('operation');

    // 自动首次查询只发一次 RPC——URL watch 不得触发第二次查询。
    expect(queryAuditEvents).toHaveBeenCalledTimes(1);
    expect(queryAuditEvents).toHaveBeenCalledWith(
      expect.objectContaining({
        filter: expect.objectContaining({
          organizationId: '',
          allOrganizations: true,
          resourceType: 'operation',
          action: 'upgrade',
        }),
      }),
    );
  });

  it('clears stale results and re-queries when the active organization changes (AC-059-06)', async () => {
    signInAs('org-1', ['viewer']);
    queryAuditEvents
      .mockResolvedValueOnce(queryResponse([auditEvent('a')]))
      .mockResolvedValueOnce(queryResponse([auditEvent('b')]));

    mountAuditPage();
    await flushPromises();
    const store = useAuditStore();
    expect(store.events.map((event) => event.id)).toEqual(['a']);

    // 服务端重签 Session 后 activeOrganization 变化 → 清空旧结果并重查新 org。
    signInAs('org-2', ['viewer']);
    await flushPromises();

    expect(queryAuditEvents).toHaveBeenCalledTimes(2);
    expect(queryAuditEvents).toHaveBeenLastCalledWith(
      expect.objectContaining({
        filter: expect.objectContaining({ organizationId: 'org-2' }),
      }),
    );
    expect(store.events.map((event) => event.id)).toEqual(['b']);
  });

  // TASK-339: the widening is platform_admin-only and explicit on the wire.
  it('queries every organization by default for platform_admin', async () => {
    signInAs('org-1');
    queryAuditEvents.mockResolvedValue(queryResponse([auditEvent('a')]));

    mountAuditPage();
    await flushPromises();

    expect(queryAuditEvents).toHaveBeenCalledTimes(1);
    const request = queryAuditEvents.mock.calls[0][0];
    expect(request.filter).toMatchObject({ allOrganizations: true, organizationId: '' });
  });

  it('neither renders the scope selector nor sends allOrganizations for a viewer', async () => {
    signInAs('org-1', ['viewer']);
    queryAuditEvents.mockResolvedValue(queryResponse([auditEvent('a')]));

    const wrapper = mountAuditPage();
    await flushPromises();

    expect(wrapper.find('select[name="organization"]').exists()).toBe(false);
    const request = queryAuditEvents.mock.calls[0][0];
    expect(request.filter).not.toHaveProperty('allOrganizations');
    expect(request.filter?.organizationId).toBe('org-1');
  });

  it('offers organizations plus the static system scope to platform_admin', async () => {
    signInAs('org-1');
    queryAuditEvents.mockResolvedValue(queryResponse([auditEvent('a')]));

    const wrapper = mountAuditPage();
    await flushPromises();

    const selector = wrapper.get<HTMLSelectElement>('select[name="organization"]');
    const values = Array.from(selector.element.options).map((option) => option.value);
    expect(values).toEqual(['', 'org-1', 'org-2', 'system']);
    expect(selector.text()).toContain('系统事件（system）');
  });

  it('queries the system scope when platform_admin selects it', async () => {
    signInAs('org-1');
    queryAuditEvents.mockResolvedValue(queryResponse([auditEvent('a')]));

    const wrapper = mountAuditPage();
    await flushPromises();

    await wrapper.get('select[name="organization"]').setValue('system');
    await flushPromises();

    expect(queryAuditEvents).toHaveBeenCalledTimes(2);
    const request = queryAuditEvents.mock.calls[1][0];
    expect(request.filter).toMatchObject({ organizationId: 'system' });
    expect(request.filter).not.toHaveProperty('allOrganizations');
  });

  // B5 regression: a failed request used to be rendered *next to* the empty
  // state, so "Audit request failed" and "No audit events" appeared together
  // and the failure read as "there is nothing to see".
  it('renders only the error state when the first query fails', async () => {
    signInAs('org-1');
    queryAuditEvents.mockRejectedValue(new ConnectError('audit unavailable', Code.Unavailable));

    const wrapper = mountAuditPage();
    await flushPromises();

    expect(wrapper.text()).toContain('审计请求失败');
    expect(wrapper.text()).not.toContain('暂无审计事件');
  });

  it('renders the 403 state for permission_denied instead of an empty result', async () => {
    signInAs('org-1');
    queryAuditEvents.mockRejectedValue(new ConnectError('denied', Code.PermissionDenied));

    const wrapper = mountAuditPage();
    await flushPromises();

    expect(wrapper.text()).toContain('403');
    expect(wrapper.text()).not.toContain('暂无审计事件');
    expect(wrapper.text()).not.toContain('Audit request failed');
  });

  it('keeps the loaded page and reports the failure inline when a refresh fails (AC-059-08)', async () => {
    signInAs('org-1');
    queryAuditEvents
      .mockResolvedValueOnce(queryResponse([auditEvent('a')]))
      .mockRejectedValueOnce(new ConnectError('audit unavailable', Code.Unavailable));

    const wrapper = mountAuditPage();
    await flushPromises();
    const store = useAuditStore();
    expect(store.events.map((event) => event.id)).toEqual(['a']);

    await store.query('org-1', 'first');
    await flushPromises();

    // The page is still there, and the failure is reported next to it.
    expect(store.events.map((event) => event.id)).toEqual(['a']);
    expect(wrapper.text()).toContain('a');
    expect(wrapper.text()).not.toContain('暂无审计事件');
  });
});
