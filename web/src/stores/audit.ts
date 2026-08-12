import { defineStore } from 'pinia';
import { computed, shallowRef } from 'vue';
import { Code, ConnectError } from '@connectrpc/connect';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { auditClient } from '@/connect/client';
import type { AuditEvent } from '@/gen/audit/v1/audit_pb';

export const defaultAuditPageSize = 20;
export const maxAuditRangeDays = 30;

// The wire contract (D-62) uses free-form strings for resource_type/action/status.
// These are the values emitted by services today; the option lists below are
// datalist suggestions, not a closed set — any string filters through to the RPC.
export const resourceTypeOptions = [
  'release_operation',
  'operation',
  'release_bundle',
  'trust_root',
  'audit_export',
  'organization',
  'release',
  'customer',
  'cluster',
];

export const actionOptions = [
  'create',
  'update',
  'delete',
  'approve',
  'reject',
  'install',
  'upgrade',
  'rollback',
  'emergency_change',
  'export.created',
  'verify_trust',
  'login',
  'logout',
];

export const statusOptions = ['succeeded', 'failed', 'accepted', 'success'];

export interface AuditFilters {
  // Private filter: never written to the URL (AC-059-05/07).
  actor: string;
  resourceType: string;
  resourceId: string;
  action: string;
  status: string;
  // Local datetime-local values (no timezone); converted to ISO8601 for the URL.
  from: string;
  to: string;
}

export type AuditFailureReason =
  | 'permission_denied'
  | 'range_too_large'
  | 'invalid_cursor'
  | 'invalid_argument'
  | 'export_unavailable'
  | 'deadline_exceeded'
  | 'internal'
  | 'unknown';

export interface AuditFailure {
  reason: AuditFailureReason;
  message: string;
  maxRangeDays?: number;
}

export interface AuditExportTask {
  // export_id from ExportAuditEventsResponse; AC-059-04 requires displaying it.
  taskId: string;
  status: string;
}

function defaultRange(): Pick<AuditFilters, 'from' | 'to'> {
  const to = new Date();
  const from = new Date(to.getTime() - 24 * 60 * 60 * 1000);
  return { from: toLocalInput(from), to: toLocalInput(to) };
}

export const emptyAuditFilters = (): AuditFilters => ({
  actor: '',
  resourceType: '',
  resourceId: '',
  action: '',
  status: '',
  ...defaultRange(),
});

function first(value: unknown): string {
  if (Array.isArray(value)) return typeof value[0] === 'string' ? value[0] : '';
  return typeof value === 'string' ? value : '';
}

// URL codec (AC-059-07): resource/resource_id/action/status/from/to round-trip;
// actor never enters the URL.
export function filtersFromQuery(query: Record<string, unknown>): AuditFilters {
  const defaults = emptyAuditFilters();
  return {
    actor: '',
    resourceType: first(query.resource),
    resourceId: first(query.resource_id),
    action: first(query.action),
    status: first(query.status),
    from: first(query.from) ? toLocalInput(new Date(first(query.from))) : defaults.from,
    to: first(query.to) ? toLocalInput(new Date(first(query.to))) : defaults.to,
  };
}

export function filtersToQuery(filters: AuditFilters): Record<string, string> {
  const query: Record<string, string> = {};
  if (filters.resourceType) query.resource = filters.resourceType;
  if (filters.resourceId) query.resource_id = filters.resourceId;
  if (filters.action) query.action = filters.action;
  if (filters.status) query.status = filters.status;
  if (filters.from) query.from = new Date(filters.from).toISOString();
  if (filters.to) query.to = new Date(filters.to).toISOString();
  return query;
}

function asDate(value: string): Date | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? undefined : date;
}

// Converts a Date to a local datetime-local input value (YYYY-MM-DDTHH:mm).
function toLocalInput(date: Date): string {
  if (Number.isNaN(date.getTime())) return '';
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}

// Client-side preflight (AC-059-02): a range wider than maxAuditRangeDays is
// rejected before any RPC is sent.
function validateRange(filters: AuditFilters): AuditFailure | null {
  const from = asDate(filters.from);
  const to = asDate(filters.to);
  if (!from || !to) return { reason: 'invalid_argument', message: 'Select a valid start and end time.' };
  if (from > to) return { reason: 'invalid_argument', message: 'The start time must not be after the end time.' };
  if (to.getTime() - from.getTime() > maxAuditRangeDays * 24 * 60 * 60 * 1000) {
    return {
      reason: 'range_too_large',
      message: `Please narrow the time range to ${maxAuditRangeDays} days or less.`,
      maxRangeDays: maxAuditRangeDays,
    };
  }
  return null;
}

function toProtoFilter(filters: AuditFilters, organizationId: string) {
  const from = asDate(filters.from);
  const to = asDate(filters.to);
  return {
    organizationId,
    resourceType: filters.resourceType.trim(),
    resourceId: filters.resourceId.trim(),
    actorId: filters.actor.trim(),
    action: filters.action.trim(),
    status: filters.status.trim(),
    timeRange: {
      start: from ? timestampFromDate(from) : undefined,
      end: to ? timestampFromDate(to) : undefined,
    },
  };
}

// Server errors are classified by the X-Reason-Code metadata header when present
// (TASK-029 v2 contract, TASK-027 pattern); otherwise the Connect code decides.
function failureFrom(error: unknown): AuditFailure {
  const connectError = ConnectError.from(error);
  const reason = connectError.metadata.get('X-Reason-Code');
  if (reason === 'permission_denied' || connectError.code === Code.PermissionDenied) {
    return { reason: 'permission_denied', message: 'You do not have access to this organization.' };
  }
  if (reason === 'range_too_large') {
    return {
      reason: 'range_too_large',
      message: `The query range is too large. Please narrow it to ${maxAuditRangeDays} days or less.`,
      maxRangeDays: maxAuditRangeDays,
    };
  }
  if (reason === 'invalid_cursor') {
    return { reason: 'invalid_cursor', message: 'This page expired. The first page was reloaded.' };
  }
  if (reason === 'export_unavailable' || connectError.code === Code.Unavailable) {
    return { reason: 'export_unavailable', message: 'Audit export is unavailable. Try again later.' };
  }
  if (connectError.code === Code.DeadlineExceeded) {
    return { reason: 'deadline_exceeded', message: 'The request timed out. Please retry.' };
  }
  if (connectError.code === Code.Internal) {
    return { reason: 'internal', message: 'The audit service failed. Please retry later.' };
  }
  if (connectError.code === Code.InvalidArgument) {
    // No stable reason header (e.g. service-side validation beyond the wire
    // contract): surface the server message verbatim.
    return { reason: 'invalid_argument', message: connectError.rawMessage || 'The query was rejected.' };
  }
  return { reason: 'unknown', message: connectError.rawMessage || 'The audit request failed.' };
}

export const useAuditStore = defineStore('audit', () => {
  const filters = shallowRef<AuditFilters>(emptyAuditFilters());
  const events = shallowRef<AuditEvent[]>([]);
  const cursor = shallowRef('');
  const nextCursor = shallowRef('');
  const cursorStack = shallowRef<string[]>([]);
  const seenEventIds = shallowRef<Set<string>>(new Set());
  const selectedEvent = shallowRef<AuditEvent | null>(null);
  const totalSize = shallowRef(0);
  const loading = shallowRef(false);
  const exporting = shallowRef(false);
  const error = shallowRef<AuditFailure | null>(null);
  const exportTasks = shallowRef<AuditExportTask[]>([]);
  const hasMore = computed(() => nextCursor.value.length > 0);
  const hasPrevious = computed(() => cursorStack.value.length > 0);

  function setFilters(nextFilters: AuditFilters): void {
    filters.value = { ...nextFilters };
  }

  function clearResults(): void {
    events.value = [];
    cursor.value = '';
    nextCursor.value = '';
    cursorStack.value = [];
    seenEventIds.value = new Set();
    selectedEvent.value = null;
    totalSize.value = 0;
  }

  async function query(organizationId: string, direction: 'first' | 'next' | 'previous' = 'first'): Promise<void> {
    const rangeError = validateRange(filters.value);
    if (rangeError) {
      error.value = rangeError;
      return;
    }
    const previousEvents = events.value;
    const previousCursor = cursor.value;
    const previousNextCursor = nextCursor.value;
    const previousCursorStack = cursorStack.value;
    const previousSeenEventIds = seenEventIds.value;
    let requestCursor = '';
    if (direction === 'next') requestCursor = nextCursor.value;
    if (direction === 'previous') requestCursor = cursorStack.value.at(-1) ?? '';

    loading.value = true;
    error.value = null;
    try {
      const response = await auditClient.queryAuditEvents({
        filter: toProtoFilter(filters.value, organizationId),
        pagination: { pageSize: defaultAuditPageSize, pageToken: requestCursor },
      });
      if (direction === 'first') {
        cursor.value = '';
        cursorStack.value = [];
        seenEventIds.value = new Set();
      } else if (direction === 'next') {
        cursorStack.value = [...cursorStack.value, cursor.value];
        cursor.value = requestCursor;
      } else {
        cursor.value = requestCursor;
        cursorStack.value = cursorStack.value.slice(0, -1);
      }
      // AC-059-03: never re-render events already seen in this session, so
      // cursor pagination stays stable when new events arrive between pages.
      const incoming = response.events.filter((event) => !seenEventIds.value.has(event.id));
      for (const event of response.events) seenEventIds.value.add(event.id);
      events.value = direction === 'next' ? [...events.value, ...incoming] : incoming;
      nextCursor.value = response.pagination?.nextPageToken ?? '';
      totalSize.value = response.pagination?.totalSize ?? events.value.length;
      selectedEvent.value = null;
    } catch (requestError) {
      const failure = failureFrom(requestError);
      error.value = failure;
      if (failure.reason === 'permission_denied') {
        // AC-059-01: filters stay, stale results must not leak other org data.
        clearResults();
      } else if (failure.reason === 'invalid_cursor') {
        clearResults();
        await query(organizationId, 'first');
      } else {
        // AC-059-08: timeout/internal errors keep the loaded page intact.
        events.value = previousEvents;
        cursor.value = previousCursor;
        nextCursor.value = previousNextCursor;
        cursorStack.value = previousCursorStack;
        seenEventIds.value = previousSeenEventIds;
      }
    } finally {
      loading.value = false;
    }
  }

  async function exportEvents(organizationId: string): Promise<void> {
    const rangeError = validateRange(filters.value);
    if (rangeError) {
      error.value = rangeError;
      return;
    }
    exporting.value = true;
    error.value = null;
    try {
      const response = await auditClient.exportAuditEvents({
        filter: toProtoFilter(filters.value, organizationId),
      });
      // The wire contract returns only a creation receipt (export_id + initial
      // status); there is no status-query RPC (D-62), so tasks never poll.
      exportTasks.value = [{ taskId: response.exportId, status: response.status }, ...exportTasks.value];
    } catch (requestError) {
      error.value = failureFrom(requestError);
    } finally {
      exporting.value = false;
    }
  }

  function selectEvent(event: AuditEvent | null): void {
    selectedEvent.value = event;
  }

  return {
    filters,
    events,
    cursor,
    nextCursor,
    cursorStack,
    seenEventIds,
    selectedEvent,
    totalSize,
    loading,
    exporting,
    error,
    exportTasks,
    hasMore,
    hasPrevious,
    setFilters,
    clearResults,
    query,
    exportEvents,
    selectEvent,
  };
});
