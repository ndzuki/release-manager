import { defineStore } from 'pinia';
import { computed, ref } from 'vue';
import { fromJsonString } from '@bufbuild/protobuf';
import type {
  OperationSnapshot as ProtoOperationSnapshot,
  TimelineEntry as ProtoTimelineEntry,
  WatchOperationResponse,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { OperationSnapshotSchema } from '@/gen/orchestrator/v1/orchestrator_pb';
import { cancelOperation, getOperation, mapOperationError, watchOperation } from '@/connect/operation-api';
import type { OperationAPIError } from '@/connect/operation-api';
import { useAuthStore } from '@/stores/auth';
import type { EffectStatus, Operation, OperationSnapshot, OperationState, TimelineEntry } from '@/types/operation';
import { mapOperationSnapshot, mapTimelineEntry } from '@/types/operation';

export type StreamStatus = 'connecting' | 'connected' | 'disconnected';
export type EmergencyEffectStatus = 'watching' | 'resolved' | 'not_applicable';

export interface OperationError {
  code: string;
  message: string;
  retryable: boolean;
}

export interface CancelSubmitResult {
  ok: boolean;
  errorCode: string | null;
  message: string;
}

export interface ConnectionOptions {
  now?: () => number;
  random?: () => number;
  liveUpdatesEnabled?: () => boolean;
  // Test seam: override the auth write-role check (defaults to the auth store).
  canWrite?: () => boolean;
  // Test seams: replace the abort-signal constructor and timer functions so
  // tests stay deterministic without touching production behavior.
  abortController?: () => AbortController;
  setTimeout?: (handler: () => void, ms: number) => unknown;
  clearTimeout?: (handle: unknown) => void;
  // Test seam: decode the base64 protojson snapshot carried by cursor_expired.
  decodeSnapshotProto?: (encoded: string) => OperationSnapshot;
}

export const MAX_TIMELINE_ENTRIES = 500;
export const NO_MESSAGE_TIMEOUT_MS = 30_000;
export const POLL_INTERVAL_MS = 5_000;
export const RECONNECT_BASE_MS = 1_000;
export const RECONNECT_MAX_MS = 30_000;
export const RECONNECT_JITTER = 0.2;
export const REASON_MIN_CHARS = 1;
export const REASON_MAX_CHARS = 500;

const EFFECT_STATES: Record<string, true> = {
  UNKNOWN: true,
  APPLIED: true,
  NOT_APPLIED: true,
  NOT_STARTED: true,
};

const OPERATION_STATES: Record<string, true> = {
  pending: true,
  preflight: true,
  queued: true,
  running: true,
  cancelling: true,
  succeeded: true,
  failed: true,
  cancelled: true,
  timeout: true,
};

const TERMINAL_STATES: Record<string, true> = {
  succeeded: true,
  failed: true,
  cancelled: true,
  timeout: true,
};

const CANCELLABLE_STANDARD_STATES: Record<string, true> = {
  pending: true,
  preflight: true,
  queued: true,
  running: true,
};

const CANCELLABLE_EMERGENCY_STATES: Record<string, true> = {
  pending: true,
  queued: true,
};

function defaultAbortController(): AbortController {
  return new AbortController();
}

function defaultTimeout(handler: () => void, ms: number): unknown {
  return setTimeout(handler, ms);
}

function defaultClearTimeout(handle: unknown): void {
  clearTimeout(handle as ReturnType<typeof setTimeout>);
}

function defaultDecodeSnapshotProto(encoded: string): OperationSnapshot {
  const binary = atob(encoded);
  const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0));
  const json = new TextDecoder().decode(bytes);
  return mapOperationSnapshot(fromJsonString(OperationSnapshotSchema, json));
}

export const useOperationTimelineStore = defineStore('operationTimeline', () => {
  const authStore = useAuthStore();

  const operationId = ref<string | null>(null);
  const operation = ref<Operation | null>(null);
  const entries = ref<TimelineEntry[]>([]);
  const latestSequence = ref(0n);
  const retainedFromSequence = ref(0n);
  const streamStatus = ref<StreamStatus>('connecting');
  const lastHeartbeatAt = ref<string | null>(null);
  const historyTruncated = ref(false);
  const historyGap = ref(false);
  const cancelLoading = ref(false);
  const activeCancelIdempotencyKey = ref<string | null>(null);
  const cancelError = ref<{ code: string; message: string } | null>(null);
  // Server denied a cancel with permission_denied: treat the entry as hidden
  // even if the local role projection still claims write (AC-057-22).
  const serverDeniedWrite = ref(false);
  const emergencyEffectStatus = ref<EmergencyEffectStatus>('not_applicable');
  const initialError = ref<OperationError | null>(null);
  const cancelDialogOpen = ref(false);
  const options = ref<ConnectionOptions>({});

  // Scope generation: incremented by load/reset only. In-flight callbacks
  // capture the current value and bail out when it moves, so refresh and
  // submitCancel never invalidate a live stream.
  let scope = 0;
  let abortController: AbortController | null = null;
  let heartbeatTimer: unknown | null = null;
  let pollTimer: unknown | null = null;
  let reconnectTimer: unknown | null = null;
  let reconnectAttempts = 0;
  let cancelledByUser = false;
  const isTerminal = computed(() => operation.value !== null && TERMINAL_STATES[operation.value.state] === true);

  // AC-057-07: no write role (local projection or server denial) → no cancel
  // UI at all, including the disabled terminal/cancelling affordances.
  const showCancel = computed(() => {
    if (serverDeniedWrite.value) return false;
    return options.value.canWrite ? options.value.canWrite() : authStore.canWrite;
  });

  const canCancel = computed(() => {
    const current = operation.value;
    if (!current) return false;
    if (!showCancel.value) return false;
    const allowed = current.operationType === 'EMERGENCY'
      ? CANCELLABLE_EMERGENCY_STATES[current.state] === true
      : CANCELLABLE_STANDARD_STATES[current.state] === true;
    return allowed;
  });

  function configure(nextOptions: ConnectionOptions): void {
    options.value = { ...options.value, ...nextOptions };
  }

  function now(): number {
    return options.value.now ? options.value.now() : Date.now();
  }

  function random(): number {
    return options.value.random ? options.value.random() : Math.random();
  }

  function setTimeoutHandle(handler: () => void, ms: number): unknown {
    return options.value.setTimeout ? options.value.setTimeout(handler, ms) : defaultTimeout(handler, ms);
  }

  function clearTimeoutHandle(handle: unknown): void {
    if (handle === null || handle === undefined) return;
    if (options.value.clearTimeout) options.value.clearTimeout(handle);
    else defaultClearTimeout(handle);
  }

  function clearAllTimers(): void {
    clearTimeoutHandle(heartbeatTimer);
    clearTimeoutHandle(pollTimer);
    clearTimeoutHandle(reconnectTimer);
    heartbeatTimer = null;
    pollTimer = null;
    reconnectTimer = null;
  }

  function disposeStream(): void {
    if (abortController) {
      abortController.abort();
      abortController = null;
    }
  }
  function scheduleHeartbeatTimeout(): void {
    clearTimeoutHandle(heartbeatTimer);
    heartbeatTimer = setTimeoutHandle(() => {
      heartbeatTimer = null;
      if (streamStatus.value === 'connected') {
        enterDisconnected();
      }
    }, NO_MESSAGE_TIMEOUT_MS);
  }

  function stopPolling(): void {
    clearTimeoutHandle(pollTimer);
    pollTimer = null;
  }

  function enterDisconnected(): void {
    if (streamStatus.value === 'disconnected') return;
    streamStatus.value = 'disconnected';
    disposeStream();
    clearTimeoutHandle(heartbeatTimer);
    heartbeatTimer = null;
    if (operationId.value && !cancelledByUser) {
      schedulePoll();
      scheduleReconnect();
    }
  }

  function schedulePoll(): void {
    if (pollTimer) return;
    pollTimer = setTimeoutHandle(() => {
      pollTimer = null;
      void refresh().then(() => {
        // Keep the unique 5s poll alive while degraded (AC-057-05/24):
        // reconnect success, scope change, or user teardown stops it.
        if (streamStatus.value === 'disconnected' && operationId.value && !cancelledByUser) {
          schedulePoll();
        }
      });
    }, POLL_INTERVAL_MS);
  }

  function scheduleReconnect(): void {
    if (reconnectTimer || !operationId.value) return;
    const attempt = reconnectAttempts++;
    const base = Math.min(RECONNECT_BASE_MS * 2 ** attempt, RECONNECT_MAX_MS);
    const jittered = base * (1 + (random() * 2 - 1) * RECONNECT_JITTER);
    reconnectTimer = setTimeoutHandle(() => {
      reconnectTimer = null;
      if (streamStatus.value === 'disconnected' && operationId.value) {
        void startWatch(operationId.value, latestSequence.value);
      }
    }, jittered);
  }

  function evaluateEmergencyEffect(current: Operation | null): void {
    // resolved is terminal: only load/reset clears it (AC-057-19/20).
    if (emergencyEffectStatus.value === 'resolved') return;
    if (
      current &&
      current.operationType === 'EMERGENCY' &&
      TERMINAL_STATES[current.state] === true &&
      current.effectStatus === 'UNKNOWN'
    ) {
      emergencyEffectStatus.value = 'watching';
      return;
    }
    emergencyEffectStatus.value = 'not_applicable';
  }

  function applySnapshot(snapshot: ProtoOperationSnapshot): boolean {
    // The stream is opened per operation; drop any mismatched snapshot so a
    // server bug can never pollute the page or clear a history gap.
    if (!snapshot.operation || snapshot.operation.operationId !== operationId.value) return false;
    const mapped = mapOperationSnapshot(snapshot);
    operation.value = mapped.operation;
    retainedFromSequence.value = mapped.retainedFromSequence;
    evaluateEmergencyEffect(mapped.operation);
    return true;
  }

  function applyEntry(entry: ProtoTimelineEntry): void {
    const current = operationId.value;
    if (!current || entry.operationId !== current) return;
    if (entries.value.some((existing) => existing.id === entry.id)) return;
    if (entry.sequence <= latestSequence.value) return;
    latestSequence.value = entry.sequence;
    const mapped = mapTimelineEntry(entry);
    entries.value = [...entries.value, mapped];
    if (entries.value.length > MAX_TIMELINE_ENTRIES) {
      entries.value = entries.value.slice(-MAX_TIMELINE_ENTRIES);
      historyTruncated.value = true;
    }
    const kind = mapped.kind;
    if (kind === 'EMERGENCY_EFFECT_RESOLVED') {
      emergencyEffectStatus.value = 'resolved';
      // The resolved entry also carries the authoritative effect outcome:
      // merge it into the summary (monotonic only) so the header shows the
      // confirmed effect instead of the stale UNKNOWN.
      if (entry.effectTo && operation.value) {
        const nextVersion = entry.operationStateVersion;
        if (EFFECT_STATES[entry.effectTo] === true && nextVersion > operation.value.stateVersion) {
          operation.value = {
            ...operation.value,
            effectStatus: entry.effectTo as EffectStatus,
            stateVersion: nextVersion,
          };
        }
      }
    } else {
      // STATE_TRANSITION carries the authoritative next state: merge it into
      // the operation summary (monotonic only) so the header, canCancel and
      // the CAS expectedStateVersion track the live stream instead of the
      // first snapshot. Out-of-order entries never
      // regress stateVersion (AC-057-01).
      if (kind === 'STATE_TRANSITION' && entry.toState && operation.value) {
        const nextVersion = entry.operationStateVersion;
        if (OPERATION_STATES[entry.toState] === true && nextVersion > operation.value.stateVersion) {
          operation.value = {
            ...operation.value,
            state: entry.toState as OperationState,
            stateVersion: nextVersion,
          };
        }
      }
      evaluateEmergencyEffect(operation.value);
    }
  }

  async function startWatch(nextOperationId: string, afterSequence: bigint): Promise<void> {
    disposeStream();
    const capturedScope = scope;
    const controller = options.value.abortController ? options.value.abortController() : defaultAbortController();
    abortController = controller;
    streamStatus.value = 'connecting';

    let nextStream: AsyncIterable<WatchOperationResponse>;
    try {
      nextStream = await watchOperation(nextOperationId, afterSequence, controller.signal);
    } catch (error) {
      if (controller.signal.aborted) return;
      handleStreamStartFailure(nextOperationId, error, capturedScope);
      return;
    }
    // Guard on both scope and operationId: a same-id reload must not let a
    // stale stream continue writing into the new scope.
    if (capturedScope !== scope || operationId.value !== nextOperationId) {
      controller.abort();
      return;
    }
    void consumeStream(nextOperationId, nextStream, controller, capturedScope);
  }

  function handleStreamStartFailure(nextOperationId: string, error: unknown, capturedScope: number): void {
    if (capturedScope !== scope || operationId.value !== nextOperationId) return;
    const mapped = mapOperationError(error);
    if (mapped.code === 'cursor_expired') {
      handleCursorExpired(nextOperationId, mapped, capturedScope);
      return;
    }
    if (mapped.code === 'not_found' || mapped.code === 'permission_denied') {
      initialError.value = { code: mapped.code, message: mapped.message, retryable: false };
      return;
    }
    if (
      mapped.code === 'network_error' ||
      mapped.code === 'stream_disconnected' ||
      mapped.code === 'dependency_unavailable'
    ) {
      // First load: Error component with retry (AC-057-25/28). Reconnect
      // after data exists: degraded mode keeps the last authoritative state.
      if (!operation.value) {
        initialError.value = { code: mapped.code, message: mapped.message, retryable: true };
        return;
      }
      enterDisconnected();
      // A failed reconnect consumed its timer slot; re-arm the next attempt
      // with doubled backoff so the store never stalls in disconnected.
      if (streamStatus.value === 'disconnected') {
        scheduleReconnect();
      }
      return;
    }
    initialError.value = { code: mapped.code, message: mapped.message, retryable: mapped.retryable };
  }

  async function consumeStream(
    nextOperationId: string,
    nextStream: AsyncIterable<WatchOperationResponse>,
    controller: AbortController,
    capturedScope: number,
  ): Promise<void> {
    try {
      for await (const message of nextStream) {
        if (capturedScope !== scope || operationId.value !== nextOperationId || controller.signal.aborted) {
          return;
        }
        switch (message.payload.case) {
          case 'snapshot':
            if (applySnapshot(message.payload.value)) {
              streamStatus.value = 'connected';
              // Re-established stream: the gap is now backfilled from the
              // carried snapshot (AC-057-29).
              historyGap.value = false;
              stopPolling();
              reconnectAttempts = 0;
              scheduleHeartbeatTimeout();
            }
            break;
          case 'entry':
            // Only an entry for the current operation is evidence of a
            // healthy stream. A stale/mismatched entry must not clear a
            // history gap, stop degraded polling, or reset backoff without
            // any accepted data.
            if (message.payload.value.operationId === nextOperationId) {
              applyEntry(message.payload.value);
              streamStatus.value = 'connected';
              historyGap.value = false;
              stopPolling();
              reconnectAttempts = 0;
              scheduleHeartbeatTimeout();
            }
            break;
          case 'heartbeat':
            lastHeartbeatAt.value = message.payload.value.sentAt
              ? new Date(Number(message.payload.value.sentAt.seconds) * 1000).toISOString()
              : new Date(now()).toISOString();
            scheduleHeartbeatTimeout();
            break;
          default:
            break;
        }
      }
      // Normal stream termination (server EOF): never strand the store in
      // 'connected' — fall back to degraded mode with 5s poll + bounded
      // reconnect, keeping the last authoritative state (AC-057-05/18/29).
      if (capturedScope === scope && operationId.value === nextOperationId && !controller.signal.aborted) {
        enterDisconnected();
      }
    } catch (error) {
      if (capturedScope !== scope || operationId.value !== nextOperationId || controller.signal.aborted) return;
      const mapped = mapOperationError(error);
      if (mapped.code === 'cursor_expired') {
        handleCursorExpired(nextOperationId, mapped, capturedScope);
        return;
      }
      if (mapped.code === 'network_error' || mapped.code === 'stream_disconnected' || mapped.code === 'dependency_unavailable') {
        enterDisconnected();
        return;
      }
      // Any other stream termination: never strand the store in 'connecting'.
      // With an operation present, degraded mode keeps the last authority.
      if (streamStatus.value !== 'disconnected') {
        enterDisconnected();
      }
    }
  }

  async function refresh(): Promise<void> {
    const currentId = operationId.value;
    if (!currentId) return;
    const capturedScope = scope;
    try {
      const latest = await getOperation(currentId);
      if (capturedScope !== scope || operationId.value !== currentId) return;
      if (!operation.value || latest.stateVersion > operation.value.stateVersion) {
        operation.value = latest;
        evaluateEmergencyEffect(latest);
      }
    } catch (error) {
      if (capturedScope !== scope || operationId.value !== currentId) return;
      const mapped = mapOperationError(error);
      // First load failed (live flag off: explicit refresh is the only path
      // in) — surface a retryable error instead of a silent empty page.
      if (!operation.value) {
        initialError.value = { code: mapped.code, message: mapped.message, retryable: mapped.retryable || mapped.code === 'network_error' };
        return;
      }
      // Transient failures keep the last authoritative state; polling retries.
    }
  }

  function handleCursorExpired(nextOperationId: string, mapped: OperationAPIError, capturedScope: number): void {
    // A stale stream's cursor error must not resurrect a rebuild after a
    // same-id reload moved the scope forward.
    if (capturedScope !== scope || operationId.value !== nextOperationId) return;
    clearAllTimers();
    disposeStream();
    entries.value = [];
    latestSequence.value = 0n;
    historyGap.value = true;
    let rebuildAfter = mapped.snapshotSequence ?? 0n;
    if (mapped.snapshotProto) {
      const decoder = options.value.decodeSnapshotProto ?? defaultDecodeSnapshotProto;
      try {
        const snapshot = decoder(mapped.snapshotProto);
        if (snapshot.operation.operationId === nextOperationId) {
          operation.value = snapshot.operation;
          retainedFromSequence.value = snapshot.retainedFromSequence;
          latestSequence.value = snapshot.snapshotSequence;
          rebuildAfter = snapshot.snapshotSequence;
          evaluateEmergencyEffect(snapshot.operation);
        }
      } catch {
        // Fall back to a fresh stream built on the carried sequence.
      }
    }
    void startWatch(nextOperationId, rebuildAfter);
  }

  function retryInitial(): void {
    const currentId = operationId.value;
    if (!currentId) return;
    initialError.value = null;
    streamStatus.value = 'connecting';
    void startWatch(currentId, 0n);
  }

  async function load(nextOperationId: string): Promise<void> {
    if (
      operationId.value === nextOperationId &&
      streamStatus.value !== 'disconnected' &&
      streamStatus.value !== 'connecting'
    ) {
      return;
    }
    cancelledByUser = false;
    scope++;
    clearAllTimers();
    disposeStream();
    operationId.value = nextOperationId;
    operation.value = null;
    entries.value = [];
    latestSequence.value = 0n;
    retainedFromSequence.value = 0n;
    streamStatus.value = 'connecting';
    lastHeartbeatAt.value = null;
    historyTruncated.value = false;
    historyGap.value = false;
    cancelLoading.value = false;
    activeCancelIdempotencyKey.value = null;
    cancelError.value = null;
    serverDeniedWrite.value = false;
    emergencyEffectStatus.value = 'not_applicable';
    initialError.value = null;
    cancelDialogOpen.value = false;
    reconnectAttempts = 0;

    if (options.value.liveUpdatesEnabled && !options.value.liveUpdatesEnabled()) {
      // Feature flag off (AC-057-16): no stream, no auto-polling, and no
      // implicit initial fetch — only the explicit refresh action loads data.
      return;
    }
    void startWatch(nextOperationId, 0n);
  }

  function reset(): void {
    cancelledByUser = true;
    scope++;
    clearAllTimers();
    disposeStream();
    operationId.value = null;
    operation.value = null;
    entries.value = [];
    latestSequence.value = 0n;
    retainedFromSequence.value = 0n;
    streamStatus.value = 'connecting';
    lastHeartbeatAt.value = null;
    historyTruncated.value = false;
    historyGap.value = false;
    cancelLoading.value = false;
    activeCancelIdempotencyKey.value = null;
    cancelError.value = null;
    serverDeniedWrite.value = false;
    emergencyEffectStatus.value = 'not_applicable';
    initialError.value = null;
    cancelDialogOpen.value = false;
    reconnectAttempts = 0;
  }

  async function submitCancel(reason: string): Promise<CancelSubmitResult> {
    const current = operation.value;
    const currentId = operationId.value;
    if (!current || !currentId) {
      return { ok: false, errorCode: 'unknown', message: '操作尚未加载完成' };
    }
    if (cancelLoading.value) {
      return { ok: false, errorCode: 'cancelling', message: '取消请求正在处理中' };
    }
    const trimmed = reason.trim();
    const charCount = [...trimmed].length;
    if (charCount < REASON_MIN_CHARS || charCount > REASON_MAX_CHARS) {
      return { ok: false, errorCode: 'invalid_argument', message: `取消原因需为 1–500 个字符（当前 ${charCount}）` };
    }
    cancelLoading.value = true;
    let key = activeCancelIdempotencyKey.value;
    if (!key) {
      key = crypto.randomUUID();
      activeCancelIdempotencyKey.value = key;
    }
    const capturedScope = scope;
    try {
      const result = await cancelOperation({
        operationId: currentId,
        reason: trimmed,
        expectedStateVersion: current.stateVersion,
        idempotencyKey: key,
      });
      if (capturedScope !== scope || operationId.value !== currentId) {
        // Scope moved on (load/reset already reinitialized cancel state);
        // the stale response must not clobber a newer scope's cancel.
        return { ok: false, errorCode: 'unknown', message: '操作已切换' };
      }
      operation.value = result.operation;
      cancelLoading.value = false;
      cancelError.value = null;
      cancelDialogOpen.value = false;
      activeCancelIdempotencyKey.value = null;
      evaluateEmergencyEffect(result.operation);
      return { ok: true, errorCode: null, message: '' };
    } catch (error) {
      if (capturedScope !== scope || operationId.value !== currentId) {
        // Same stale-response guard as the success path: never write back
        // cancelLoading/error state into a scope that moved on.
        return { ok: false, errorCode: 'unknown', message: '操作已切换' };
      }
      const mapped = mapOperationError(error);
      cancelLoading.value = false;
      cancelError.value = { code: mapped.code, message: mapped.message };
      if (mapped.code === 'optimistic_lock_conflict' || mapped.code === 'cancel_not_allowed') {
        // Business rejection: the request content changed (refreshed
        // stateVersion), so the previous key must not be reused — the
        // backend hashes expected_state_version into the idempotency record
        // and would return a conflict for the same key with a new hash.
        // Only transient/network retries reuse the key (AC-057-06).
        activeCancelIdempotencyKey.value = null;
        await refresh();
      }
      if (mapped.code === 'permission_denied') {
        // Server denied write despite the local role projection: hide the entry.
        serverDeniedWrite.value = true;
      }
      return { ok: false, errorCode: mapped.code, message: mapped.message };
    }
  }

  return {
    operationId,
    operation,
    entries,
    latestSequence,
    retainedFromSequence,
    streamStatus,
    lastHeartbeatAt,
    historyTruncated,
    historyGap,
    cancelLoading,
    activeCancelIdempotencyKey,
    cancelError,
    emergencyEffectStatus,
    initialError,
    cancelDialogOpen,
    isTerminal,
    showCancel,
    canCancel,
    configure,
    load,
    refresh,
    retryInitial,
    submitCancel,
    reset,
  };
});
