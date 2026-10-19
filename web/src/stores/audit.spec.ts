import { createPinia, setActivePinia } from 'pinia';
import { Code, ConnectError } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { auditClient } from '@/connect/client';
import { useAuthStore } from '@/stores/auth';
import { emptyAuditFilters, filtersFromQuery, filtersToQuery, useAuditStore } from './audit';
import type { AuditEvent, ExportAuditEventsResponse, QueryAuditEventsResponse } from '@/gen/audit/v1/audit_pb';

vi.mock('@/connect/client', () => ({
  auditClient: {
    queryAuditEvents: vi.fn(),
    exportAuditEvents: vi.fn(),
  },
  // The audit store composes the auth store (TASK-344), whose setup registers this
  // handler; without the export the import would fail.
  authClient: { switchOrganization: vi.fn() },
  setAuthErrorHandler: vi.fn(),
}));

const queryAuditEvents = vi.mocked(auditClient.queryAuditEvents);
const exportAuditEvents = vi.mocked(auditClient.exportAuditEvents);

const ORG = 'org-1';

// The preflight window follows the caller's role (TASK-344): the audit store reads
// `auth.isPlatformAdmin`, so tests pin the session roles the same way the console
// receives them.
function signInAs(roles: string[]): void {
  useAuthStore().$patch({
    status: 'authenticated',
    user: {
      $typeName: 'auth.v1.SessionUser' as const,
      id: 'u1',
      username: 'someone',
      roles,
      activeOrgId: ORG,
    },
  });
}

function auditEvent(id: string, overrides: Partial<AuditEvent> = {}): AuditEvent {
  return {
    $typeName: 'audit.v1.AuditEvent' as const,
    id,
    actor: {
      $typeName: 'audit.v1.AuditActor' as const,
      kind: 2,
      id: `user-${id}`,
      organizationId: ORG,
      role: 'release_admin',
    },
    resourceType: 'operation',
    resourceId: `operation-${id}`,
    action: 'upgrade',
    status: 'succeeded',
    durationMs: 10n,
    changeSummary: '',
    metadata: {},
    ...overrides,
  };
}

function queryResponse(events: AuditEvent[], nextPageToken = ''): QueryAuditEventsResponse {
  return {
    $typeName: 'audit.v1.QueryAuditEventsResponse' as const,
    events,
    pagination: { $typeName: 'common.v1.PaginationResponse' as const, nextPageToken, totalSize: 100 },
  };
}


function connectError(code: Code, message: string, reason?: string): ConnectError {
  const error = new ConnectError(message, code);
  if (reason) error.metadata.set('X-Reason-Code', reason);
  return error;
}

describe('audit query store', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    queryAuditEvents.mockReset();
    exportAuditEvents.mockReset();
  });

  it('keeps only non-sensitive filters in the URL (AC-059-07)', () => {
    const filters = {
      ...emptyAuditFilters(),
      actor: 'user-1',
      resourceType: 'operation',
      resourceId: 'op-1',
      action: 'upgrade',
      status: 'succeeded',
      from: '2026-07-01T00:00',
      to: '2026-07-02T00:00',
    };
    const query = filtersToQuery(filters);
    expect(query.resource).toBe('operation');
    expect(query.resource_id).toBe('op-1');
    expect(query.action).toBe('upgrade');
    expect(query.status).toBe('succeeded');
    expect(query.from).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/);
    expect(query.to).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/);
    expect('actor' in query).toBe(false);
    expect('organization_id' in query).toBe(false);
    expect('page_size' in query).toBe(false);
  });

  it('restores filters from URL query params (AC-059-07)', () => {
    const filters = filtersFromQuery({
      resource: 'operation',
      resource_id: 'op-1',
      action: 'upgrade',
      status: 'succeeded',
      from: '2026-07-01T00:00:00.000Z',
      to: '2026-07-02T00:00:00.000Z',
    });
    expect(filters.resourceType).toBe('operation');
    expect(filters.resourceId).toBe('op-1');
    expect(filters.action).toBe('upgrade');
    expect(filters.status).toBe('succeeded');
    expect(filters.actor).toBe('');
  });

  it('blocks a range wider than the default 31-day window without sending an RPC (AC-344-02/04)', async () => {
    const store = useAuditStore();
    store.setFilters({
      ...emptyAuditFilters(),
      // 32 days: one day past release-auth's defaultMaxWindowDays.
      from: '2026-06-01T00:00',
      to: '2026-07-03T00:00',
    });
    await store.query(ORG);
    expect(store.error?.reason).toBe('range_too_large');
    expect(store.error?.message).toContain('31');
    expect(queryAuditEvents).not.toHaveBeenCalled();
  });

  it('allows a 31-day range for a non-admin, matching the server boundary (AC-344-02/03)', async () => {
    const store = useAuditStore();
    store.setFilters({
      ...emptyAuditFilters(),
      from: '2026-06-01T00:00',
      to: '2026-07-02T00:00',
    });
    queryAuditEvents.mockResolvedValueOnce(queryResponse([]));
    await store.query(ORG);
    expect(store.error).toBeNull();
    expect(queryAuditEvents).toHaveBeenCalledTimes(1);
  });

  it('allows a 100-day range for platform_admin (AC-344-01)', async () => {
    signInAs(['platform_admin']);
    const store = useAuditStore();
    expect(store.maxRangeDays).toBe(366);
    store.setFilters({
      ...emptyAuditFilters(),
      from: '2026-01-01T00:00',
      to: '2026-04-11T00:00',
    });
    queryAuditEvents.mockResolvedValueOnce(queryResponse([]));
    await store.query(ORG);
    expect(store.error).toBeNull();
    expect(queryAuditEvents).toHaveBeenCalledTimes(1);
  });

  it('allows a 366-day range for platform_admin and blocks 367 (AC-344-01/03)', async () => {
    signInAs(['platform_admin']);
    const store = useAuditStore();
    store.setFilters({
      ...emptyAuditFilters(),
      from: '2026-01-01T00:00',
      to: '2027-01-02T00:00',
    });
    queryAuditEvents.mockResolvedValueOnce(queryResponse([]));
    await store.query(ORG);
    expect(store.error).toBeNull();
    expect(queryAuditEvents).toHaveBeenCalledTimes(1);

    store.setFilters({
      ...emptyAuditFilters(),
      from: '2026-01-01T00:00',
      to: '2027-01-03T00:00',
    });
    await store.query(ORG);
    expect(store.error?.reason).toBe('range_too_large');
    expect(store.error?.message).toContain('366');
    expect(queryAuditEvents).toHaveBeenCalledTimes(1);
  });

  it('reports the server range_too_large with the platform_admin window (AC-344-01/04)', async () => {
    signInAs(['platform_admin']);
    const store = useAuditStore();
    store.setFilters({
      ...emptyAuditFilters(),
      from: '2026-01-01T00:00',
      to: '2026-04-11T00:00',
    });
    queryAuditEvents.mockRejectedValueOnce(connectError(Code.InvalidArgument, 'range too large', 'range_too_large'));
    await store.query(ORG);
    expect(store.error?.reason).toBe('range_too_large');
    // A server refusal must state the effective role window, not a hardcoded 30.
    expect(store.error?.message).toContain('366');
  });

  it('maps a server range_too_large rejection to an inline failure (AC-059-02)', async () => {
    const store = useAuditStore();
    store.setFilters({
      ...emptyAuditFilters(),
      from: '2026-06-01T00:00',
      to: '2026-07-02T00:00',
    });
    queryAuditEvents.mockRejectedValueOnce(connectError(Code.InvalidArgument, 'range too large', 'range_too_large'));
    await store.query(ORG);
    expect(store.error?.reason).toBe('range_too_large');
    expect(store.events).toHaveLength(0);
  });

  it('clears events when the API rejects cross-organization access, keeping filters (AC-059-01)', async () => {
    const store = useAuditStore();
    store.setFilters({ ...emptyAuditFilters(), resourceType: 'operation' });
    queryAuditEvents.mockResolvedValueOnce(queryResponse([auditEvent('a')]));
    await store.query(ORG);
    expect(store.events).toHaveLength(1);

    queryAuditEvents.mockRejectedValueOnce(connectError(Code.PermissionDenied, 'denied', 'permission_denied'));
    await store.query(ORG);
    expect(store.events).toHaveLength(0);
    expect(store.filters.resourceType).toBe('operation');
    expect(store.error?.reason).toBe('permission_denied');
  });

  it('deduplicates cursor pages against all previously seen event IDs (AC-059-03)', async () => {
    const store = useAuditStore();
    queryAuditEvents.mockResolvedValueOnce(queryResponse([auditEvent('a'), auditEvent('b')], 'cursor-1'));
    await store.query(ORG);
    expect(store.events.map((event) => event.id)).toEqual(['a', 'b']);
    expect(store.hasMore).toBe(true);

    // New event 'c' arrives before the next page; 'b' repeats on page two.
    queryAuditEvents.mockResolvedValueOnce(queryResponse([auditEvent('b'), auditEvent('c')], ''));
    await store.query(ORG, 'next');
    // TASK-375: the page is REPLACED, and the session dedupe still drops the id
    // page one already showed — page two holds only what page one did not.
    expect(store.events.map((event) => event.id)).toEqual(['c']);
    expect(store.seenEventIds.has('a')).toBe(true);
    expect(store.seenEventIds.has('b')).toBe(true);
    expect(store.seenEventIds.has('c')).toBe(true);
    expect(store.hasMore).toBe(false);
    expect(store.pageNumber).toBe(2);
  });

  it('restores the previous page verbatim when paging backward (AC-059-03)', async () => {
    const store = useAuditStore();
    queryAuditEvents
      .mockResolvedValueOnce(queryResponse([auditEvent('a'), auditEvent('b')], 'cursor-1'))
      .mockResolvedValueOnce(queryResponse([auditEvent('b'), auditEvent('c')], 'cursor-2'))
      .mockResolvedValueOnce(queryResponse([auditEvent('a'), auditEvent('b')], 'cursor-1'));
    await store.query(ORG);
    await store.query(ORG, 'next');
    // TASK-375: page two replaces page one; 'b' was already seen on page one.
    expect(store.events.map((event) => event.id)).toEqual(['c']);
    expect(store.pageNumber).toBe(2);

    // 回看已浏览页必须原样恢复，不得被全会话去重清空。
    await store.query(ORG, 'previous');
    expect(store.events.map((event) => event.id)).toEqual(['a', 'b']);
    expect(store.hasPrevious).toBe(false);
    expect(store.hasMore).toBe(true);
    expect(store.pageNumber).toBe(1);
  });

  /*
   * TASK-377: stepping back used to pour the revisited page's ids into the
   * session-wide `seenEventIds`, so the next forward step filtered that page
   * away entirely. Measured in production (TASK-376 acceptance) on a 39-event /
   * 2-page query: 1 -> 2 -> previous -> next showed the empty "暂无审计事件".
   */
  it('keeps the target page populated when stepping back and forward again (TASK-377)', async () => {
    const store = useAuditStore();
    const page1 = Array.from({ length: 20 }, (_, index) => auditEvent(`p1-${index}`));
    const page2 = Array.from({ length: 19 }, (_, index) => auditEvent(`p2-${index}`));
    queryAuditEvents
      .mockResolvedValueOnce(queryResponse(page1, 'cursor-1'))
      .mockResolvedValueOnce(queryResponse(page2, ''))
      .mockResolvedValueOnce(queryResponse(page1, 'cursor-1'))
      .mockResolvedValueOnce(queryResponse(page2, ''));

    await store.query(ORG);
    await store.query(ORG, 'next');
    expect(store.events.map((event) => event.id)).toEqual(page2.map((event) => event.id));
    expect(store.pageNumber).toBe(2);

    await store.query(ORG, 'previous');
    expect(store.events.map((event) => event.id)).toEqual(page1.map((event) => event.id));
    expect(store.pageNumber).toBe(1);

    // The regression: page two must come back intact, not collapse into the
    // empty state after the revisit.
    await store.query(ORG, 'next');
    expect(store.events.map((event) => event.id)).toEqual(page2.map((event) => event.id));
    expect(store.events).toHaveLength(19);
    expect(store.pageNumber).toBe(2);
    expect(store.hasMore).toBe(false);
    expect(store.hasPrevious).toBe(true);
  });

  it('walks back and forth through three pages without emptying any page (TASK-377)', async () => {
    const store = useAuditStore();
    const pages = [
      Array.from({ length: 20 }, (_, index) => auditEvent(`p1-${index}`)),
      Array.from({ length: 20 }, (_, index) => auditEvent(`p2-${index}`)),
      Array.from({ length: 19 }, (_, index) => auditEvent(`p3-${index}`)),
    ];
    queryAuditEvents
      .mockResolvedValueOnce(queryResponse(pages[0], 'cursor-1'))
      .mockResolvedValueOnce(queryResponse(pages[1], 'cursor-2'))
      .mockResolvedValueOnce(queryResponse(pages[2], ''))
      .mockResolvedValueOnce(queryResponse(pages[1], 'cursor-2'))
      .mockResolvedValueOnce(queryResponse(pages[0], 'cursor-1'))
      .mockResolvedValueOnce(queryResponse(pages[1], 'cursor-2'))
      .mockResolvedValueOnce(queryResponse(pages[2], ''));

    const steps = [
      { direction: 'first', page: 1 },
      { direction: 'next', page: 2 },
      { direction: 'next', page: 3 },
      { direction: 'previous', page: 2 },
      { direction: 'previous', page: 1 },
      { direction: 'next', page: 2 },
      { direction: 'next', page: 3 },
    ] as const;
    for (const step of steps) {
      await store.query(ORG, step.direction);
      expect(store.pageNumber, `${step.direction} -> page ${step.page}`).toBe(step.page);
      expect(store.events, `${step.direction} -> page ${step.page}`).toEqual(pages[step.page - 1]);
    }
  });

  it('drops the previous chain from the seen set when new filters start a new query (TASK-377)', async () => {
    const store = useAuditStore();
    queryAuditEvents.mockResolvedValueOnce(queryResponse([auditEvent('a'), auditEvent('b')], 'cursor-1'));
    await store.query(ORG);
    expect([...store.seenEventIds].sort()).toEqual(['a', 'b']);

    store.setFilters({ ...emptyAuditFilters(), action: 'delete' });
    queryAuditEvents.mockResolvedValueOnce(queryResponse([auditEvent('c')], ''));
    await store.query(ORG);
    // A filter change restarts the cursor chain: no id from the old chain may
    // survive, or the first forward step of the new chain would be filtered.
    expect([...store.seenEventIds]).toEqual(['c']);
  });

  /*
   * TASK-375 (Lead ruling: replace, not "load more"). `next` used to append while
   * `previous` replaced, so stepping back from page three kept only that one page's
   * rows (measured: 60 rows -> 20, 40 rows lost) and the cursor stack could not
   * describe where the user was. The pager's own labels are "上一页/下一页", and
   * AC-059-03 constrains duplicates only — it never asks for accumulation.
   */
  it('replaces the page on every cursor step instead of accumulating (TASK-375)', async () => {
    const store = useAuditStore();
    const page1 = Array.from({ length: 20 }, (_, index) => auditEvent(`p1-${index}`));
    const page2 = Array.from({ length: 20 }, (_, index) => auditEvent(`p2-${index}`));
    const page3 = Array.from({ length: 20 }, (_, index) => auditEvent(`p3-${index}`));
    queryAuditEvents
      .mockResolvedValueOnce(queryResponse(page1, 'cursor-1'))
      .mockResolvedValueOnce(queryResponse(page2, 'cursor-2'))
      .mockResolvedValueOnce(queryResponse(page3, ''));

    await store.query(ORG);
    expect(store.events).toHaveLength(20);

    await store.query(ORG, 'next');
    // Appending would leave 40 rows; page two is exactly page two's 20 rows.
    expect(store.events.map((event) => event.id)).toEqual(page2.map((event) => event.id));
    expect(store.events).toHaveLength(20);
    expect(store.pageNumber).toBe(2);

    await store.query(ORG, 'next');
    expect(store.events.map((event) => event.id)).toEqual(page3.map((event) => event.id));
    expect(store.events).toHaveLength(20);
    expect(store.pageNumber).toBe(3);
  });

  it('pushes the cursor on next and pops it on previous, keeping pageNumber in step (TASK-375)', async () => {
    const store = useAuditStore();
    queryAuditEvents
      .mockResolvedValueOnce(queryResponse([auditEvent('a')], 'cursor-1'))
      .mockResolvedValueOnce(queryResponse([auditEvent('b')], 'cursor-2'))
      .mockResolvedValueOnce(queryResponse([auditEvent('c')], 'cursor-3'))
      .mockResolvedValueOnce(queryResponse([auditEvent('b')], 'cursor-2'))
      .mockResolvedValueOnce(queryResponse([auditEvent('a')], 'cursor-1'));

    await store.query(ORG);
    expect(store.cursorStack).toEqual([]);
    expect(store.pageNumber).toBe(1);

    await store.query(ORG, 'next');
    expect(store.cursorStack).toEqual(['']);
    expect(store.cursor).toBe('cursor-1');
    expect(store.pageNumber).toBe(2);

    await store.query(ORG, 'next');
    expect(store.cursorStack).toEqual(['', 'cursor-1']);
    expect(store.cursor).toBe('cursor-2');
    expect(store.pageNumber).toBe(3);

    await store.query(ORG, 'previous');
    expect(store.cursorStack).toEqual(['']);
    expect(store.cursor).toBe('cursor-1');
    expect(store.pageNumber).toBe(2);

    await store.query(ORG, 'previous');
    expect(store.cursorStack).toEqual([]);
    expect(store.cursor).toBe('');
    expect(store.pageNumber).toBe(1);
  });

  it('requests the next page with the server cursor (AC-059-03)', async () => {
    const store = useAuditStore();
    queryAuditEvents.mockResolvedValueOnce(queryResponse([auditEvent('a')], 'cursor-9'));
    await store.query(ORG);
    queryAuditEvents.mockResolvedValueOnce(queryResponse([auditEvent('d')], ''));
    await store.query(ORG, 'next');
    expect(queryAuditEvents).toHaveBeenLastCalledWith(
      expect.objectContaining({
        pagination: expect.objectContaining({ pageToken: 'cursor-9', pageSize: 20 }),
      }),
    );
  });

  it('preserves loaded events when a query times out (AC-059-08)', async () => {
    const store = useAuditStore();
    queryAuditEvents.mockResolvedValueOnce(queryResponse([auditEvent('a')], 'cursor-1'));
    await store.query(ORG);
    expect(store.events).toHaveLength(1);

    queryAuditEvents.mockRejectedValueOnce(connectError(Code.DeadlineExceeded, 'deadline'));
    await store.query(ORG, 'next');
    expect(store.events.map((event) => event.id)).toEqual(['a']);
    expect(store.error?.reason).toBe('deadline_exceeded');
  });

  it('creates an export receipt with the correlation ID and status (AC-059-04)', async () => {
    const store = useAuditStore();
    exportAuditEvents.mockResolvedValueOnce({ $typeName: 'audit.v1.ExportAuditEventsResponse' as const, exportId: 'export-1', status: 'pending' } satisfies ExportAuditEventsResponse);
    await store.exportEvents(ORG);
    expect(store.exportTasks[0]).toEqual({ taskId: 'export-1', status: 'pending' });
    expect(exportAuditEvents).toHaveBeenCalledWith(
      expect.objectContaining({
        filter: expect.objectContaining({ organizationId: ORG }),
      }),
    );
  });

  it('surfaces export_unavailable without clearing query results (AC-059-04/08)', async () => {
    const store = useAuditStore();
    queryAuditEvents.mockResolvedValueOnce(queryResponse([auditEvent('a')]));
    await store.query(ORG);

    exportAuditEvents.mockRejectedValueOnce(connectError(Code.Unavailable, 'export down', 'export_unavailable'));
    await store.exportEvents(ORG);
    expect(store.error?.reason).toBe('export_unavailable');
    expect(store.events).toHaveLength(1);
    expect(store.exportTasks).toHaveLength(0);
  });

  it('resets to the first page after an invalid cursor rejection', async () => {
    const store = useAuditStore();
    queryAuditEvents.mockResolvedValueOnce(queryResponse([auditEvent('a')], 'cursor-stale'));
    await store.query(ORG);
    queryAuditEvents.mockRejectedValueOnce(connectError(Code.InvalidArgument, 'cursor expired', 'invalid_cursor'));
    queryAuditEvents.mockResolvedValueOnce(queryResponse([auditEvent('a')], 'cursor-2'));
    await store.query(ORG, 'next');
    expect(store.error).toBeNull();
    expect(queryAuditEvents).toHaveBeenCalledTimes(3);
    expect(queryAuditEvents).toHaveBeenLastCalledWith(
      expect.objectContaining({ pagination: expect.objectContaining({ pageToken: '' }) }),
    );
  });
});
