import { createPinia, setActivePinia } from 'pinia';
import { Code, ConnectError } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { auditClient } from '@/connect/client';
import { emptyAuditFilters, filtersFromQuery, filtersToQuery, useAuditStore } from './audit';

vi.mock('@/connect/client', () => ({
  auditClient: {
    queryAuditEvents: vi.fn(),
    exportAuditEvents: vi.fn(),
  },
}));

const queryAuditEvents = vi.mocked(auditClient.queryAuditEvents);
const exportAuditEvents = vi.mocked(auditClient.exportAuditEvents);

const ORG = 'org-1';

function auditEvent(id: string, overrides: Partial<Record<string, unknown>> = {}) {
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

function queryResponse(events: ReturnType<typeof auditEvent>[], nextPageToken = '') {
  return {
    events,
    pagination: { nextPageToken, totalSize: 100 },
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

  it('blocks ranges over 30 days without sending an RPC (AC-059-02)', async () => {
    const store = useAuditStore();
    store.setFilters({
      ...emptyAuditFilters(),
      from: '2026-06-01T00:00',
      to: '2026-07-02T00:00',
    });
    await store.query(ORG);
    expect(store.error?.reason).toBe('range_too_large');
    expect(queryAuditEvents).not.toHaveBeenCalled();
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
    expect(store.events.map((event) => event.id)).toEqual(['a', 'b', 'c']);
    expect(store.hasMore).toBe(false);
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
    exportAuditEvents.mockResolvedValueOnce({ exportId: 'export-1', status: 'pending' });
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
