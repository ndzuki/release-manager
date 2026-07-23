import { defineStore } from 'pinia';
import { computed, shallowRef } from 'vue';
import { Code, ConnectError } from '@connectrpc/connect';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { auditClient } from '@/connect/client';
import {
  ActionType,
  AuditErrorDetailSchema,
  ExportFormat,
  ResourceType,
  StatusType,
  type AuditEvent,
} from '@/gen/audit/v1/audit_pb';

export const defaultAuditPageSize = 20;
export const maxAuditRangeDays = 30;

const actionByName: Record<string, ActionType> = {
  CREATE: ActionType.CREATE,
  UPDATE: ActionType.UPDATE,
  DELETE: ActionType.DELETE,
  APPROVE: ActionType.APPROVE,
  REJECT: ActionType.REJECT,
  INSTALL: ActionType.INSTALL,
  UPGRADE: ActionType.UPGRADE,
  ROLLBACK: ActionType.ROLLBACK,
  EMERGENCY: ActionType.EMERGENCY,
  EXPORT: ActionType.EXPORT,
  LOGIN: ActionType.LOGIN,
  LOGOUT: ActionType.LOGOUT,
};

const statusByName: Record<string, StatusType> = {
  SUCCESS: StatusType.SUCCESS,
  FAILURE: StatusType.FAILURE,
};

const resourceByName: Record<string, ResourceType> = {
  CLUSTER: ResourceType.CLUSTER,
  RELEASE_DEFINITION: ResourceType.RELEASE_DEFINITION,
  OPERATION: ResourceType.OPERATION,
  VALUES_REVISION: ResourceType.VALUES_REVISION,
  OPERATOR: ResourceType.OPERATOR,
  TRUST_ROOT: ResourceType.TRUST_ROOT,
  TRUST_POLICY: ResourceType.TRUST_POLICY,
  CUSTOMER: ResourceType.CUSTOMER,
};

export const resourceOptions = Object.keys(resourceByName);
export const actionOptions = Object.keys(actionByName);
export const statusOptions = Object.keys(statusByName);

export interface AuditFilters {
  actor: string;
  resourceType: string;
  resourceId: string;
  actions: string[];
  statuses: string[];
  operationId: string;
  from: string;
  to: string;
}

export type AuditFailureReason =
  | 'permission_denied'
  | 'range_too_large'
  | 'invalid_cursor'
  | 'export_unavailable'
  | 'deadline_exceeded'
  | 'internal'
  | 'unknown';

export interface AuditFailure {
  reason: AuditFailureReason;
  message: string;
  maxRangeDays?: number;
  retryAfter?: string;
}

export interface AuditExportTask {
  taskId: string;
  status: string;
  downloadUrl: string;
  errorMessage: string;
  createdAt: string;
  completedAt: string;
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
  actions: [],
  statuses: [],
  operationId: '',
  ...defaultRange(),
});

function first(value: unknown): string {
  if (Array.isArray(value)) return typeof value[0] === 'string' ? value[0] : '';
  return typeof value === 'string' ? value : '';
}

function all(value: unknown): string[] {
  if (Array.isArray(value)) return value.filter((item): item is string => typeof item === 'string');
  return typeof value === 'string' ? [value] : [];
}

export function filtersFromQuery(query: Record<string, unknown>): AuditFilters {
  const defaults = emptyAuditFilters();
  return {
    actor: '',
    resourceType: first(query.resource).toUpperCase(),
    resourceId: first(query.resource_id),
    actions: all(query.action).map((value) => value.toUpperCase()).filter((value) => value in actionByName),
    statuses: all(query.status).map((value) => value.toUpperCase()).filter((value) => value in statusByName),
    operationId: first(query.operation_id),
    from: first(query.from) ? toLocalInput(new Date(first(query.from))) : defaults.from,
    to: first(query.to) ? toLocalInput(new Date(first(query.to))) : defaults.to,
  };
}

export function filtersToQuery(filters: AuditFilters): Record<string, string | string[]> {
  const query: Record<string, string | string[]> = {};
  if (filters.resourceType) query.resource = filters.resourceType;
  if (filters.resourceId) query.resource_id = filters.resourceId;
  if (filters.actions.length > 0) query.action = [...filters.actions];
  if (filters.statuses.length > 0) query.status = [...filters.statuses];
  if (filters.operationId) query.operation_id = filters.operationId;
  if (filters.from) query.from = new Date(filters.from).toISOString();
  if (filters.to) query.to = new Date(filters.to).toISOString();
  return query;
}

function asDate(value: string): Date | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? undefined : date;
}

function toLocalInput(date: Date): string {
  if (Number.isNaN(date.getTime())) return '';
  const offset = date.getTimezoneOffset() * 60_000;
  return new Date(date.getTime() - offset).toISOString().slice(0, 16);
}

function validateRange(filters: AuditFilters): AuditFailure | null {
  const from = asDate(filters.from);
  const to = asDate(filters.to);
  if (!from || !to) return { reason: 'range_too_large', message: 'Select a valid start and end time.' };
  if (from > to) return { reason: 'range_too_large', message: 'The start time must not be after the end time.' };
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
    actorId: filters.actor.trim(),
    resourceTypeEnum: resourceByName[filters.resourceType] ?? ResourceType.UNSPECIFIED,
    resourceId: filters.resourceId.trim(),
    actions: filters.actions.map((value) => actionByName[value]).filter((value) => value !== undefined),
    statuses: filters.statuses.map((value) => statusByName[value]).filter((value) => value !== undefined),
    operationId: filters.operationId.trim(),
    timeRange: {
      start: from ? timestampFromDate(from) : undefined,
      end: to ? timestampFromDate(to) : undefined,
    },
  };
}

function failureFrom(error: unknown): AuditFailure {
  const connectError = ConnectError.from(error);
  const detail = connectError.findDetails(AuditErrorDetailSchema)[0];
  const reason = detail?.reason || connectError.metadata.get('X-Reason-Code');
  if (reason === 'permission_denied' || connectError.code === Code.PermissionDenied) {
    return { reason: 'permission_denied', message: 'You do not have access to this organization.' };
  }
  if (reason === 'range_too_large') {
    const maxRangeDays = detail?.maxRangeDays || maxAuditRangeDays;
    return {
      reason: 'range_too_large',
      message: `The query range is too large. Please narrow it to ${maxRangeDays} days or less.`,
      maxRangeDays,
    };
  }
  if (reason === 'invalid_cursor') {
    return { reason: 'invalid_cursor', message: 'This page expired. The first page was reloaded.' };
  }
  if (reason === 'export_unavailable' || connectError.code === Code.Unavailable) {
    return {
      reason: 'export_unavailable',
      message: detail?.retryAfter
        ? `Audit export is unavailable. Retry after ${detail.retryAfter}.`
        : 'Audit export is unavailable. Try again later.',
      retryAfter: detail?.retryAfter,
    };
  }
  if (connectError.code === Code.DeadlineExceeded) {
    return { reason: 'deadline_exceeded', message: 'The request timed out. Please retry.' };
  }
  if (connectError.code === Code.Internal) {
    return { reason: 'internal', message: 'The audit service failed. Please retry later.' };
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
    filters.value = { ...nextFilters, actions: [...nextFilters.actions], statuses: [...nextFilters.statuses] };
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
        clearResults();
      } else if (failure.reason === 'invalid_cursor') {
        clearResults();
        await query(organizationId, 'first');
      } else {
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
        format: ExportFormat.CSV,
        maxRows: 10000,
      });
      exportTasks.value = [
        {
          taskId: response.taskId || response.exportId,
          status: response.status,
          downloadUrl: '',
          errorMessage: '',
          createdAt: response.createdAt ? response.createdAt.toString() : '',
          completedAt: '',
        },
        ...exportTasks.value,
      ];
    } catch (requestError) {
      error.value = failureFrom(requestError);
    } finally {
      exporting.value = false;
    }
  }

  async function refreshExport(taskId: string): Promise<void> {
    try {
      const response = await auditClient.getAuditExportStatus({ taskId });
      exportTasks.value = exportTasks.value.map((task) =>
        task.taskId === taskId
          ? {
              taskId,
              status: response.status,
              downloadUrl: response.downloadUrl,
              errorMessage: response.errorMessage,
              createdAt: response.createdAt ? response.createdAt.toString() : task.createdAt,
              completedAt: response.completedAt ? response.completedAt.toString() : '',
            }
          : task,
      );
    } catch (requestError) {
      error.value = failureFrom(requestError);
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
    refreshExport,
    selectEvent,
  };
});
