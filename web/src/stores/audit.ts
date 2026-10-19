import { t } from '@/i18n/messages';
import { defineStore } from 'pinia';
import { computed, shallowRef } from 'vue';
import { Code, ConnectError } from '@connectrpc/connect';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { auditClient } from '@/connect/client';
import { useAuthStore } from '@/stores/auth';
import type { AuditEvent } from '@/gen/audit/v1/audit_pb';

export const defaultAuditPageSize = 20;

// The audit query window is policy-owned by release-auth (REQ-029: 31 days for
// every role, 366 for platform_admin) and returned per request as
// `max_window_days` (api/proto/auth/v1/auth.proto). The console does not read that
// decision on this page yet, so it mirrors the two policy ceilings to keep the
// preflight honest. The server remains the authority: it re-checks every query and
// export and answers INVALID_ARGUMENT / range_too_large, which `failureFrom` still
// surfaces with the effective window (TASK-344).
export const defaultAuditRangeDays = 31;
export const platformAdminAuditRangeDays = 366;

export function auditRangeDays(platformAdmin: boolean): number {
  return platformAdmin ? platformAdminAuditRangeDays : defaultAuditRangeDays;
}

// TASK-339: the platform-level audit scope (internal/store.SystemOrganizationID).
// It is a sentinel stored in audit_events.organization_id, not an organization
// row, so the scope selector offers it as a static entry.
export const systemOrganizationId = 'system';

// The wire contract (D-62) uses free-form strings for resource_type/action/status.
// The option lists below populate the filter form's selects; any string still
// filters through to the RPC (server-side values are not a closed set).
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
  | 'all_organizations_not_supported'
  | 'export_unavailable'
  | 'deadline_exceeded'
  | 'internal'
  | 'unknown';

export interface AuditFailure {
  reason: AuditFailureReason;
  message: string;
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

// Client-side preflight (TASK-344 supersedes the fixed 30-day AC-059-02 check): a
// range wider than the role's policy window is rejected before any RPC is sent.
// This is a UX shortcut only; the server re-checks the same span against
// release-auth's `max_window_days`.
function validateRange(filters: AuditFilters, maxRangeDays: number): AuditFailure | null {
  const from = asDate(filters.from);
  const to = asDate(filters.to);
  if (!from || !to) return { reason: 'invalid_argument', message: t('audit.error.range') };
  if (from > to) return { reason: 'invalid_argument', message: t('audit.error.rangeOrder') };
  if (to.getTime() - from.getTime() > maxRangeDays * 24 * 60 * 60 * 1000) {
    return {
      reason: 'range_too_large',
      message: t('audit.error.narrowDays', { days: String(maxRangeDays) }),
    };
  }
  return null;
}

function toProtoFilter(filters: AuditFilters, organizationId: string, allOrganizations = false) {
  const from = asDate(filters.from);
  const to = asDate(filters.to);
  return {
    // TASK-339: the all-organizations scope is a platform_admin-only widening.
    // It is sent only when requested, so a non-platform_admin request body never
    // carries the field at all, and it replaces organization_id (the server
    // ignores organization_id when the flag is set).
    ...(allOrganizations ? { allOrganizations: true } : {}),
    organizationId: allOrganizations ? '' : organizationId,
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
function failureFrom(error: unknown, maxRangeDays: number): AuditFailure {
  const connectError = ConnectError.from(error);
  const reason = connectError.metadata.get('X-Reason-Code');
  if (reason === 'permission_denied' || connectError.code === Code.PermissionDenied) {
    return { reason: 'permission_denied', message: t('audit.error.noAccess') };
  }
  if (reason === 'range_too_large') {
    return {
      reason: 'range_too_large',
      message: t('audit.error.rangeTooLarge', { days: String(maxRangeDays) }),
    };
  }
  if (reason === 'invalid_cursor') {
    return { reason: 'invalid_cursor', message: t('audit.error.expired') };
  }
  // TASK-339 review: the server explicitly refuses an all-organizations export
  // (an export row carries one organization). The reason code has its own copy;
  // without this branch the raw English server message was shown to the user.
  if (reason === 'all_organizations_not_supported') {
    return {
      reason: 'all_organizations_not_supported',
      message: t('audit.error.allOrganizationsExport'),
    };
  }
  if (reason === 'export_unavailable' || connectError.code === Code.Unavailable) {
    return { reason: 'export_unavailable', message: t('audit.error.exportUnavailable') };
  }
  if (connectError.code === Code.DeadlineExceeded) {
    return { reason: 'deadline_exceeded', message: t('audit.error.timeout') };
  }
  if (connectError.code === Code.Internal) {
    return { reason: 'internal', message: t('audit.error.service') };
  }
  if (connectError.code === Code.InvalidArgument) {
    // No stable reason header (e.g. service-side validation beyond the wire
    // contract): surface the server message verbatim.
    return { reason: 'invalid_argument', message: connectError.rawMessage || t('audit.error.rejected') };
  }
  return { reason: 'unknown', message: connectError.rawMessage || t('audit.error.requestFailed') };
}

export const useAuditStore = defineStore('audit', () => {
  const auth = useAuthStore();
  const filters = shallowRef<AuditFilters>(emptyAuditFilters());
  const events = shallowRef<AuditEvent[]>([]);
  const cursor = shallowRef('');
  const nextCursor = shallowRef('');
  const cursorStack = shallowRef<string[]>([]);
  // TASK-377: `seenEventIds` dedupes FORWARD steps only, and its scope is the
  // forward chain currently on screen — not the whole session. `pageIdStack`
  // runs parallel to `cursorStack` with one extra entry: an id list per page
  // walked, the current page included. `seenEventIds` is derived from it, so
  // stepping back can drop the page being left. Previously every response —
  // backward ones too — was poured into the session-wide set, so revisiting
  // page 2 filtered its rows away (TASK-376 measured 1 -> 2 -> previous -> next
  // as an empty "暂无审计事件" with 0 rows).
  const seenEventIds = shallowRef<Set<string>>(new Set());
  const pageIdStack = shallowRef<string[][]>([]);
  const selectedEvent = shallowRef<AuditEvent | null>(null);
  const totalSize = shallowRef(0);
  const loading = shallowRef(false);
  const exporting = shallowRef(false);
  const error = shallowRef<AuditFailure | null>(null);
  const exportTasks = shallowRef<AuditExportTask[]>([]);
  const hasMore = computed(() => nextCursor.value.length > 0);
  const hasPrevious = computed(() => cursorStack.value.length > 0);
  // TASK-375: 1-based page index for the pager's visible/announced position. The
  // stack holds one cursor per page already walked, so its length is the number of
  // pages before this one — the same relation OperationCenterPage uses
  // (`cursorIndex + 1`), and it resets to 1 whenever the stack is cleared.
  const pageNumber = computed(() => cursorStack.value.length + 1);
  // The UI affordance behind the preflight: the server still decides (it returns
  // `max_window_days` per request), so this only keeps the console from refusing a
  // range the caller is actually allowed to query (TASK-344).
  const maxRangeDays = computed(() => auditRangeDays(auth.isPlatformAdmin));

  function setFilters(nextFilters: AuditFilters): void {
    filters.value = { ...nextFilters };
  }

  function clearResults(): void {
    events.value = [];
    cursor.value = '';
    nextCursor.value = '';
    cursorStack.value = [];
    seenEventIds.value = new Set();
    pageIdStack.value = [];
    selectedEvent.value = null;
    totalSize.value = 0;
  }

  async function query(
    organizationId: string,
    direction: 'first' | 'next' | 'previous' = 'first',
    allOrganizations = false,
  ): Promise<void> {
    const rangeError = validateRange(filters.value, maxRangeDays.value);
    if (rangeError) {
      error.value = rangeError;
      return;
    }
    const previousEvents = events.value;
    const previousCursor = cursor.value;
    const previousNextCursor = nextCursor.value;
    const previousCursorStack = cursorStack.value;
    const previousSeenEventIds = seenEventIds.value;
    const previousPageIdStack = pageIdStack.value;
    let requestCursor = '';
    if (direction === 'next') requestCursor = nextCursor.value;
    if (direction === 'previous') requestCursor = cursorStack.value.at(-1) ?? '';

    loading.value = true;
    error.value = null;
    try {
      const response = await auditClient.queryAuditEvents({
        filter: toProtoFilter(filters.value, organizationId, allOrganizations),
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
      // AC-059-03: when paging FORWARD, never re-render events already seen on
      // the chain, so cursor pagination stays stable when new events arrive
      // between pages. Backward pages are restored verbatim — deduplicating
      // them against the seen set would wipe the list.
      const incoming =
        direction === 'next'
          ? response.events.filter((event) => !seenEventIds.value.has(event.id))
          : response.events;
      // TASK-377: keep one id list per walked page, aligned with `cursorStack`
      // (the current page is the last entry). `seenEventIds` is rebuilt from the
      // chain instead of being accumulated forever.
      const pageIds = incoming.map((event) => event.id);
      if (direction === 'first') {
        // A new query opens a new forward chain.
        pageIdStack.value = [pageIds];
      } else if (direction === 'next') {
        pageIdStack.value = [...pageIdStack.value, pageIds];
      } else {
        // Backward: drop the page being left and re-record the restored page
        // from what is actually rendered, so a later forward step starts from
        // this page's rows only.
        const stack = pageIdStack.value;
        const target = Math.max(0, stack.length - 2);
        pageIdStack.value = [...stack.slice(0, target), pageIds];
      }
      seenEventIds.value = new Set(pageIdStack.value.flat());
      // TASK-375: one page at a time — REPLACE, never append. Appending made the
      // mixed model below observable ("next" accumulated, "previous" replaced) and
      // made "previous" lose every row already accumulated once the cursor stack
      // had more than one entry. The labels and the cursor stack
      // (`cursorStack: 历史游标栈，支持"上一页"`) describe paging, not "load more";
      // AC-059-03 only constrains duplicates, and the dedupe above still holds.
      events.value = incoming;
      nextCursor.value = response.pagination?.nextPageToken ?? '';
      totalSize.value = response.pagination?.totalSize ?? events.value.length;
      selectedEvent.value = null;
    } catch (requestError) {
      const failure = failureFrom(requestError, maxRangeDays.value);
      error.value = failure;
      if (failure.reason === 'permission_denied') {
        // AC-059-01: filters stay, stale results must not leak other org data.
        clearResults();
      } else if (failure.reason === 'invalid_cursor') {
        clearResults();
        await query(organizationId, 'first', allOrganizations);
      } else {
        // AC-059-08: timeout/internal errors keep the loaded page intact.
        events.value = previousEvents;
        cursor.value = previousCursor;
        nextCursor.value = previousNextCursor;
        cursorStack.value = previousCursorStack;
        seenEventIds.value = previousSeenEventIds;
        pageIdStack.value = previousPageIdStack;
      }
    } finally {
      loading.value = false;
    }
  }

  async function exportEvents(organizationId: string, allOrganizations = false): Promise<void> {
    const rangeError = validateRange(filters.value, maxRangeDays.value);
    if (rangeError) {
      error.value = rangeError;
      return;
    }
    exporting.value = true;
    error.value = null;
    try {
      const response = await auditClient.exportAuditEvents({
        filter: toProtoFilter(filters.value, organizationId, allOrganizations),
      });
      // The wire contract returns only a creation receipt (export_id + initial
      // status); there is no status-query RPC (D-62), so tasks never poll.
      exportTasks.value = [{ taskId: response.exportId, status: response.status }, ...exportTasks.value];
    } catch (requestError) {
      error.value = failureFrom(requestError, maxRangeDays.value);
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
    pageNumber,
    maxRangeDays,
    setFilters,
    clearResults,
    query,
    exportEvents,
    selectEvent,
  };
});
