import { createPinia, setActivePinia } from 'pinia';
import { Code, ConnectError } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { auditClient } from '@/connect/client';
import { emptyAuditFilters, filtersFromQuery, filtersToQuery, useAuditStore } from './audit';

vi.mock('@/connect/client', () => ({
  auditClient: {
    queryAuditEvents: vi.fn(),
    exportAuditEvents: vi.fn(),
    getAuditExportStatus: vi.fn(),
  },
}));

const queryAuditEvents = vi.mocked(auditClient.queryAuditEvents);
const exportAuditEvents = vi.mocked(auditClient.exportAuditEvents);
const getAuditExportStatus = vi.mocked(auditClient.getAuditExportStatus);

function auditEvent(id: string) {
  return {
    $typeName: 'audit.v1.AuditEvent' as const,
    id,
    resourceType: 'OPERATION',
    resourceId: `operation-${id}`,
    action: 'UPGRADE',
    status: 'SUCCESS',
    durationMs: 10n,
    changeSummary: '',
    metadata: {},
    operationId: `operation-${id}`,
    requestId: `request-${id}`,
  };
}

describe('audit query store', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    queryAuditEvents.mockReset();
    exportAuditEvents.mockReset();
    getAuditExportStatus.mockReset();
  });

  it('keeps only non-sensitive filters in the URL', () => {
    const filters = {
      ...emptyAuditFilters(),
      actor: 'user-1',
      resourceType: 'OPERATION',
      resourceId: 'operation-1',
      actions: ['UPGRADE', 'ROLLBACK'],
      statuses: ['SUCCESS'],
      operationId: 'operation-1',
      from: '2026-07-22T00:00',
      to: '2026-07-23T00:00',
    };

    const query = filtersToQuery(filters);
    const restored = filtersFromQuery(query);

    expect(query).toEqual({
      resource: 'OPERATION',
      resource_id: 'operation-1',
      action: ['UPGRADE', 'ROLLBACK'],
      status: ['SUCCESS'],
      operation_id: 'operation-1',
      from: new Date(filters.from).toISOString(),
      to: new Date(filters.to).toISOString(),
    });
    expect(query).not.toHaveProperty('actor');
    expect(query).not.toHaveProperty('organization_id');
    expect(restored.actor).toBe('');
    expect(restored.actions).toEqual(filters.actions);
    expect(restored.statuses).toEqual(filters.statuses);
    expect(restored.operationId).toBe(filters.operationId);
  });

  it('blocks ranges over 30 days without sending an RPC', async () => {
    const store = useAuditStore();
    store.setFilters({
      ...emptyAuditFilters(),
      from: '2026-06-01T00:00',
      to: '2026-07-23T00:00',
    });

    await store.query('org-1');

    expect(queryAuditEvents).not.toHaveBeenCalled();
    expect(store.error?.reason).toBe('range_too_large');
    expect(store.error?.message).toContain('30 days');
  });

  it('clears events when the API rejects cross-organization access', async () => {
    const store = useAuditStore();
    store.events = [auditEvent('existing')];
    queryAuditEvents.mockRejectedValue(
      new ConnectError('permission_denied', Code.PermissionDenied, { 'X-Reason-Code': 'permission_denied' }),
    );

    await store.query('org-1');

    expect(store.events).toEqual([]);
    expect(store.error).toEqual({
      reason: 'permission_denied',
      message: 'You do not have access to this organization.',
    });
  });

  it('deduplicates cursor pages against all previously seen event IDs', async () => {
    const store = useAuditStore();
    queryAuditEvents
      .mockResolvedValueOnce({
        $typeName: 'audit.v1.QueryAuditEventsResponse',
        events: [auditEvent('event-2'), auditEvent('event-1')],
        pagination: {
          $typeName: 'common.v1.PaginationResponse',
          nextPageToken: 'cursor-2',
          totalSize: 3,
        },
      })
      .mockResolvedValueOnce({
        $typeName: 'audit.v1.QueryAuditEventsResponse',
        events: [auditEvent('event-1'), auditEvent('event-0')],
        pagination: {
          $typeName: 'common.v1.PaginationResponse',
          nextPageToken: '',
          totalSize: 3,
        },
      });

    await store.query('org-1');
    await store.query('org-1', 'next');

    expect(store.events.map((event) => event.id)).toEqual(['event-2', 'event-1', 'event-0']);
    expect([...store.seenEventIds]).toEqual(['event-2', 'event-1', 'event-0']);
    expect(queryAuditEvents.mock.calls[1]?.[0].pagination?.pageToken).toBe('cursor-2');
    expect(store.hasMore).toBe(false);
  });

  it('preserves loaded events when a query times out', async () => {
    const store = useAuditStore();
    store.events = [auditEvent('existing')];
    queryAuditEvents.mockRejectedValue(new ConnectError('timed out', Code.DeadlineExceeded));

    await store.query('org-1');

    expect(store.events.map((event) => event.id)).toEqual(['existing']);
    expect(store.error?.reason).toBe('deadline_exceeded');
  });

  it('creates and refreshes asynchronous export tasks', async () => {
    const store = useAuditStore();
    exportAuditEvents.mockResolvedValue({
      $typeName: 'audit.v1.ExportAuditEventsResponse',
      exportId: 'audit-export-42',
      taskId: 'audit-export-42',
      status: 'pending',
    });
    getAuditExportStatus.mockResolvedValue({
      $typeName: 'audit.v1.GetAuditExportStatusResponse',
      taskId: 'audit-export-42',
      status: 'ready',
      downloadUrl: '/exports/audit-export-42.csv',
      errorMessage: '',
    });

    await store.exportEvents('org-1');
    await store.refreshExport('audit-export-42');

    expect(store.exportTasks).toEqual([
      expect.objectContaining({
        taskId: 'audit-export-42',
        status: 'ready',
        downloadUrl: '/exports/audit-export-42.csv',
      }),
    ]);
  });
});
