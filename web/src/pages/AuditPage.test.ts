import { flushPromises, mount } from '@vue/test-utils';
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

function signInAs(activeOrgId: string): void {
  const auth = useAuthStore();
  auth.status = 'authenticated';
  auth.user = {
    $typeName: 'auth.v1.SessionUser' as const,
    id: 'u1',
    username: 'admin',
    roles: ['platform_admin'],
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
        filter: expect.objectContaining({ organizationId: 'org-1', resourceType: 'operation', action: 'upgrade' }),
      }),
    );
  });

  it('clears stale results and re-queries when the active organization changes (AC-059-06)', async () => {
    signInAs('org-1');
    queryAuditEvents
      .mockResolvedValueOnce(queryResponse([auditEvent('a')]))
      .mockResolvedValueOnce(queryResponse([auditEvent('b')]));

    mountAuditPage();
    await flushPromises();
    const store = useAuditStore();
    expect(store.events.map((event) => event.id)).toEqual(['a']);

    // 服务端重签 Session 后 activeOrganization 变化 → 清空旧结果并重查新 org。
    signInAs('org-2');
    await flushPromises();

    expect(queryAuditEvents).toHaveBeenCalledTimes(2);
    expect(queryAuditEvents).toHaveBeenLastCalledWith(
      expect.objectContaining({
        filter: expect.objectContaining({ organizationId: 'org-2' }),
      }),
    );
    expect(store.events.map((event) => event.id)).toEqual(['b']);
  });
});
