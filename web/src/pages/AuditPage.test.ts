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

function queryResponse(events: AuditEvent[], nextPageToken = '', totalSize = 100): QueryAuditEventsResponse {
  return {
    $typeName: 'audit.v1.QueryAuditEventsResponse' as const,
    events,
    pagination: { $typeName: 'common.v1.PaginationResponse' as const, nextPageToken, totalSize },
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

function mountAuditPage(options: Record<string, unknown> = {}): ReturnType<typeof mount> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/audit', name: 'Audit', component: AuditPage }],
  });
  return mount(AuditPage, { global: { plugins: [router] }, ...options });
}

function pagerButton(wrapper: ReturnType<typeof mount>, label: '上一页' | '下一页') {
  const button = wrapper.findAll('nav.pagination button').find((candidate) => candidate.text() === label);
  if (!button) throw new Error(`pager button ${label} not found`);
  return button;
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

  it('shows the policy window that matches the caller role (AC-344-01/02)', async () => {
    signInAs('org-1', ['platform_admin']);
    queryAuditEvents.mockResolvedValue(queryResponse([]));
    const adminPage = mountAuditPage();
    await flushPromises();
    expect(adminPage.get('.audit-filters__hint').text()).toContain('366');

    // A non-admin caller keeps release-auth's default 31-day window.
    setActivePinia(createPinia());
    signInAs('org-1', ['release_admin']);
    const memberPage = mountAuditPage();
    await flushPromises();
    expect(memberPage.get('.audit-filters__hint').text()).toContain('31');
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

  // TASK-339 review P1: paging is a new request, so it must reuse the scope the
  // page on screen was queried with. Calling query() without the third argument
  // sent the next page as the active organization while the table still said
  // "all organizations" — wrong data, no error.
  it('keeps the all-organizations scope when paging in both directions', async () => {
    signInAs('org-1');
    queryAuditEvents
      .mockResolvedValueOnce(queryResponse([auditEvent('a')], 'cursor-2'))
      .mockResolvedValueOnce(queryResponse([auditEvent('b')], 'cursor-3'))
      .mockResolvedValueOnce(queryResponse([auditEvent('a')]));

    const wrapper = mountAuditPage();
    await flushPromises();

    await pagerButton(wrapper, '下一页').trigger('click');
    await flushPromises();

    expect(queryAuditEvents).toHaveBeenCalledTimes(2);
    const nextRequest = queryAuditEvents.mock.calls[1][0];
    expect(nextRequest.pagination?.pageToken).toBe('cursor-2');
    expect(nextRequest.filter).toMatchObject({ allOrganizations: true, organizationId: '' });

    await pagerButton(wrapper, '上一页').trigger('click');
    await flushPromises();

    expect(queryAuditEvents).toHaveBeenCalledTimes(3);
    const previousRequest = queryAuditEvents.mock.calls[2][0];
    expect(previousRequest.pagination?.pageToken).toBe('');
    expect(previousRequest.filter).toMatchObject({ allOrganizations: true, organizationId: '' });
  });

  /*
   * TASK-375: three user-visible defects of the audit pager in one flow.
   *
   *  - "下一页" replaced the page (it used to append 20 rows to the 20 already on
   *    screen, which the label never promised);
   *  - the pager showed/announced no position at all;
   *  - focus fell to <body> as soon as the activated control became disabled (the
   *    last page disables "next"), leaving keyboard and screen-reader users with no
   *    place on the results.
   */
  it('replaces the page, announces the position and keeps focus on the results (TASK-375)', async () => {
    signInAs('org-1');
    const page1 = Array.from({ length: 20 }, (_, index) => auditEvent(`p1-${index}`));
    const page2 = Array.from({ length: 20 }, (_, index) => auditEvent(`p2-${index}`));
    queryAuditEvents
      .mockResolvedValueOnce(queryResponse(page1, 'cursor-1', 40))
      .mockResolvedValueOnce(queryResponse(page2, '', 40))
      .mockResolvedValueOnce(queryResponse(page1, 'cursor-1', 40));

    const wrapper = mountAuditPage({ attachTo: document.body });
    await flushPromises();

    expect(wrapper.findAll('tbody tr')).toHaveLength(20);
    expect(wrapper.get('.pagination__page').text()).toBe('第 1 页，共 40 条');

    await pagerButton(wrapper, '下一页').trigger('click');
    await flushPromises();

    const nextRows = wrapper.findAll('tbody tr');
    // Accumulating would leave 40 rows; page two is the 20 rows of page two.
    expect(nextRows).toHaveLength(20);
    expect(nextRows[0]!.text()).toContain('p2-0');
    expect(wrapper.text()).not.toContain('p1-0');
    const indicator = wrapper.get('.pagination__page');
    expect(indicator.text()).toBe('第 2 页，共 40 条');
    expect(indicator.attributes('role')).toBe('status');
    // Focus is moved to the named results region, never left on <body>.
    expect(document.activeElement).toBe(wrapper.get('section.audit-results').element);
    expect(document.activeElement).not.toBe(document.body);

    // "上一页" restores page one verbatim and rewinds the position.
    await pagerButton(wrapper, '上一页').trigger('click');
    await flushPromises();

    expect(wrapper.findAll('tbody tr')).toHaveLength(20);
    expect(wrapper.findAll('tbody tr')[0]!.text()).toContain('p1-0');
    expect(wrapper.get('.pagination__page').text()).toBe('第 1 页，共 40 条');
    expect(document.activeElement).toBe(wrapper.get('section.audit-results').element);

    wrapper.unmount();
  });

  // TASK-339 review P1: the static "system" scope must survive paging too.
  it('keeps the system scope when paging forward', async () => {
    signInAs('org-1');
    queryAuditEvents.mockResolvedValue(queryResponse([auditEvent('a')], 'cursor-2'));

    const wrapper = mountAuditPage();
    await flushPromises();

    await wrapper.get('select[name="organization"]').setValue('system');
    await flushPromises();
    expect(queryAuditEvents).toHaveBeenCalledTimes(2);

    await pagerButton(wrapper, '下一页').trigger('click');
    await flushPromises();

    expect(queryAuditEvents).toHaveBeenCalledTimes(3);
    const request = queryAuditEvents.mock.calls[2][0];
    expect(request.pagination?.pageToken).toBe('cursor-2');
    expect(request.filter).toMatchObject({ organizationId: 'system' });
    expect(request.filter).not.toHaveProperty('allOrganizations');
  });

  // TASK-339 review P2: an export row carries exactly one organization, so the
  // server refuses all_organizations. The console disables the action up front
  // and explains why, instead of letting the click fail with an English message.
  it('disables export with a hint while the scope is every organization', async () => {
    signInAs('org-1');
    queryAuditEvents.mockResolvedValue(queryResponse([auditEvent('a')]));

    const wrapper = mountAuditPage();
    await flushPromises();

    const exportButton = wrapper.get('header button');
    expect(exportButton.attributes('disabled')).toBeDefined();
    expect(exportButton.attributes('title')).toContain('选择一个具体组织');
    expect(wrapper.text()).toContain('导出只支持单个组织');

    await wrapper.get('select[name="organization"]').setValue('org-2');
    await flushPromises();

    expect(wrapper.get('header button').attributes('disabled')).toBeUndefined();
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

/*
 * UX-011 (W16): the form → URL mirror used to run per keystroke. The URL is
 * shareable state, not a page query, so five keystrokes must not become five
 * history writes; one debounced write is the contract.
 */
describe('AuditPage URL mirror (UX-011)', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    queryAuditEvents.mockReset();
  });

  it('writes the URL once for a burst of keystrokes instead of once per key', async () => {
    vi.useFakeTimers();
    try {
      signInAs('org-1');
      queryAuditEvents.mockResolvedValue(queryResponse([]));
      const router = createRouter({
        history: createMemoryHistory(),
        routes: [{ path: '/audit', name: 'Audit', component: AuditPage }],
      });
      await router.push('/audit');
      await router.isReady();
      const replace = vi.spyOn(router, 'replace').mockResolvedValue(undefined as never);

      const wrapper = mount(AuditPage, { global: { plugins: [router] } });
      await flushPromises();

      // `actor` is deliberately private (never mirrored into the URL), so type
      // into a field the mirror does carry.
      const resourceId = wrapper.get('input[name="resource_id"]');
      for (const value of ['a', 'ab', 'abc', 'abcd', 'abcde']) {
        await resourceId.setValue(value);
      }
      // Still inside the debounce window: nothing has been written yet.
      expect(replace).not.toHaveBeenCalled();

      await vi.advanceTimersByTimeAsync(350);
      expect(replace).toHaveBeenCalledTimes(1);
      const target = replace.mock.calls[0][0] as { query?: Record<string, string> };
      expect(target.query?.resource_id).toBe('abcde');
    } finally {
      vi.useRealTimers();
    }
  });
});

/*
 * TASK-394 (direction C audit page): the result area has exactly one state.
 *
 * The regression this locks was real (B5): the error state sat OUTSIDE the
 * loading/empty chain, so a failed request rendered "审计请求失败" next to
 * "暂无审计事件", and the reader saw a failure described as "there is nothing here".
 * The page now derives ONE `viewState` (`loading | forbidden | error | empty | data`)
 * and every branch is a `v-if`/`v-else-if` on it.
 *
 * `renderedStates` is the assertion that makes "only one" mechanical: it collects the
 * root element of every state component that is actually in the DOM, so a future edit
 * cannot pass by adding a state rather than replacing one.
 */
function renderedStates(wrapper: ReturnType<typeof mount>): string[] {
  const states = [
    ['error', '.error-state'],
    ['empty', '.empty-state'],
    ['loading', '.loading-state'],
    ['forbidden', '.forbidden-state'],
  ] as const;
  return states.filter(([, selector]) => wrapper.find(selector).exists()).map(([name]) => name);
}

describe('AuditPage result-state exclusivity (TASK-394)', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    queryAuditEvents.mockReset();
  });

  it('renders only the error state on a failed first query — never the empty copy', async () => {
    signInAs('org-1');
    queryAuditEvents.mockRejectedValue(new ConnectError('audit unavailable', Code.Unavailable));

    const wrapper = mountAuditPage();
    await flushPromises();

    expect(renderedStates(wrapper)).toEqual(['error']);
    expect(wrapper.text()).toContain('审计请求失败');
    // The empty copy must not be readable next to a failure: empty is a positive
    // statement about the data ("there are none"), not a fallback for an error.
    expect(wrapper.text()).not.toContain('暂无审计事件');
    expect(wrapper.text()).not.toContain('当前组织与筛选条件下没有匹配事件');
  });

  it('renders only the empty state on an empty result — never an error message', async () => {
    signInAs('org-1');
    queryAuditEvents.mockResolvedValue(queryResponse([]));

    const wrapper = mountAuditPage();
    await flushPromises();

    expect(renderedStates(wrapper)).toEqual(['empty']);
    expect(wrapper.text()).toContain('暂无审计事件');
    expect(wrapper.text()).not.toContain('审计请求失败');
    expect(wrapper.text()).not.toContain('查询被拒绝');
  });

  it('shows loading alone while the first query is in flight, then replaces it', async () => {
    signInAs('org-1');
    let resolveQuery: (value: QueryAuditEventsResponse) => void = () => {};
    queryAuditEvents.mockImplementation(
      () =>
        new Promise<QueryAuditEventsResponse>((resolve) => {
          resolveQuery = resolve;
        }),
    );

    const wrapper = mountAuditPage();

    // No flushPromises: this is the state while the request is still pending.
    expect(renderedStates(wrapper)).toEqual(['loading']);
    expect(wrapper.text()).not.toContain('暂无审计事件');
    expect(wrapper.text()).not.toContain('审计请求失败');

    resolveQuery(queryResponse([]));
    await flushPromises();

    // The loading state is replaced, not left on screen with the empty one.
    expect(renderedStates(wrapper)).toEqual(['empty']);
  });

  it('renders only the 403 state for a permission refusal', async () => {
    signInAs('org-1');
    queryAuditEvents.mockRejectedValue(new ConnectError('denied', Code.PermissionDenied));

    const wrapper = mountAuditPage();
    await flushPromises();

    expect(renderedStates(wrapper)).toEqual(['forbidden']);
    expect(wrapper.text()).toContain('403');
    expect(wrapper.text()).not.toContain('暂无审计事件');
    expect(wrapper.text()).not.toContain('审计请求失败');
  });
});

/*
 * TASK-394: the filter bar is a visual migration, so the request it builds is pinned
 * here field by field. Each expectation names the audit.v1.AuditQueryFilter field the
 * control maps to (api/proto/audit/v1/audit.proto): actor_id, resource_type,
 * resource_id, action, status and time_range. There is no free-text search field
 * because the contract has none.
 */
describe('AuditPage filter → AuditQueryFilter mapping (TASK-394)', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    queryAuditEvents.mockReset();
  });

  it('sends every filter control under its contract field name', async () => {
    signInAs('org-1', ['viewer']);
    queryAuditEvents.mockResolvedValue(queryResponse([]));

    const wrapper = mountAuditPage();
    await flushPromises();
    expect(queryAuditEvents).toHaveBeenCalledTimes(1);

    await wrapper.get('input[name="actor"]').setValue('alice');
    await wrapper.get('select[name="resource"]').setValue('operation');
    await wrapper.get('input[name="resource_id"]').setValue('op-1');
    await wrapper.get('select[name="action"]').setValue('upgrade');
    await wrapper.get('select[name="status"]').setValue('succeeded');
    await wrapper.get('input[name="from"]').setValue('2026-10-19T00:00');
    await wrapper.get('input[name="to"]').setValue('2026-10-19T12:00');

    await wrapper.get('form.audit-filters').trigger('submit');
    await flushPromises();

    expect(queryAuditEvents).toHaveBeenCalledTimes(2);
    expect(queryAuditEvents).toHaveBeenLastCalledWith(
      expect.objectContaining({
        filter: expect.objectContaining({
          organizationId: 'org-1',
          actorId: 'alice',
          resourceType: 'operation',
          resourceId: 'op-1',
          action: 'upgrade',
          status: 'succeeded',
          timeRange: expect.objectContaining({
            start: expect.anything(),
            end: expect.anything(),
          }),
        }),
      }),
    );
  });
});
