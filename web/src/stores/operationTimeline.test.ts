import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createPinia, setActivePinia } from 'pinia';
import { create } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import {
  OperationSchema,
  OperationSnapshotSchema,
  OperationStatus,
  TimelineEntrySchema,
  TimelineEntryKind,
  WatchOperationResponseSchema,
  EmergencyEffectStatus,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import type { WatchOperationResponse } from '@/gen/orchestrator/v1/orchestrator_pb';
import { useOperationTimelineStore } from './operationTimeline';
import * as api from '@/connect/operation-api';

vi.mock('@/connect/operation-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return {
    ...original,
    watchOperation: vi.fn(),
    getOperation: vi.fn(),
    cancelOperation: vi.fn(),
  };
});

function snapshot(operationId: string, stateVersion: bigint, sequence: bigint, overrides: Record<string, unknown> = {}) {
  return create(WatchOperationResponseSchema, {
    payload: {
      case: 'snapshot',
      value: create(OperationSnapshotSchema, {
        operation: create(OperationSchema, {
          operationId,
          operationType: 'INSTALL',
          state: OperationStatus.RUNNING,
          stateVersion,
          ...(overrides as object),
        }),
        snapshotSequence: sequence,
        retainedFromSequence: 1n,
      }),
    },
  });
}

function entry(operationId: string, sequence: bigint, overrides: Record<string, unknown> = {}) {
  return create(WatchOperationResponseSchema, {
    payload: {
      case: 'entry',
      value: create(TimelineEntrySchema, {
        id: `entry-${sequence}`,
        operationId,
        sequence,
        operationStateVersion: 1n,
        kind: TimelineEntryKind.STATE_TRANSITION,
        ...(overrides as object),
      }),
    },
  });
}

function streamOf(...messages: WatchOperationResponse[]): AsyncIterable<WatchOperationResponse> {
  return (async function* () {
    for (const message of messages) yield message;
    // Real server streams stay open until the consumer aborts: keep the
    // generator pending so tests exercise the connected state. EOF tests
    // use eofStreamOf with a deliberately finite stream instead.
    await new Promise<void>(() => {});
  })();
}

function eofStreamOf(...messages: WatchOperationResponse[]): AsyncIterable<WatchOperationResponse> {
  return (async function* () {
    for (const message of messages) yield message;
  })();
}

function cursorExpiredError(snapshotSequence: bigint, snapshotProto?: string): ConnectError {
  const headers: Record<string, string> = {
    'X-Reason-Code': 'cursor_expired',
    'X-Snapshot-Sequence': snapshotSequence.toString(),
    'X-Retained-From-Sequence': '10',
  };
  if (snapshotProto !== undefined) {
    headers['X-Snapshot-Proto'] = snapshotProto;
  }
  return new ConnectError('cursor_expired: retained timeline no longer includes the requested sequence', Code.OutOfRange, headers);
}

async function flush(): Promise<void> {
  // Async generators need one microtask turn per stream message.
  for (let i = 0; i < 1200; i++) {
    await Promise.resolve();
  }
}

const mockedWatch = vi.mocked(api.watchOperation);
const mockedGet = vi.mocked(api.getOperation);
const mockedCancel = vi.mocked(api.cancelOperation);

describe('operationTimeline store', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    vi.useFakeTimers();
    mockedWatch.mockReset();
    mockedGet.mockReset();
    mockedCancel.mockReset();
  });

  function setupStore() {
    const store = useOperationTimelineStore();
    store.configure({
      canWrite: () => true,
      random: () => 0,
      decodeSnapshotProto: () => ({
        operation: {
          operationId: 'op-1',
          releaseDefinitionId: 'def-1',
          operationType: 'EMERGENCY' as const,
          state: 'cancelled' as const,
          stateVersion: 5n,
          bundleId: '',
          valuesRevisionId: '',
          expectedRevision: 0,
          targetRevision: 0,
          createdBy: '',
          createdAt: null,
          updatedAt: null,
          terminalAt: null,
          deadline: null,
          lastError: '',
          effectStatus: 'UNKNOWN' as const,
        },
        snapshotSequence: 42n,
        retainedFromSequence: 10n,
      }),
    });
    return store;
  }

  it('AC-26: first load opens WatchOperation(after_sequence=0) without GetOperation', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n)));

    await store.load('op-1');
    await flush();

    expect(mockedWatch).toHaveBeenCalledTimes(1);
    expect(mockedWatch.mock.calls[0]![0]).toBe('op-1');
    expect(mockedWatch.mock.calls[0]![1]).toBe(0n);
    expect(mockedGet).not.toHaveBeenCalled();
    expect(store.operation?.state).toBe('running');
    expect(store.streamStatus).toBe('connected');
  });

  it('AC-10: the snapshot is the authority and replay/live entries across its boundary are all kept', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(
      snapshot('op-1', 5n, 10n),
      entry('op-1', 9n),  // replay: sequence below the snapshot boundary
      entry('op-1', 11n), // live: sequence above the snapshot boundary
    ));

    await store.load('op-1');
    await flush();

    // Snapshot drives the authoritative summary; no GetOperation gap-fill.
    expect(mockedGet).not.toHaveBeenCalled();
    expect(store.operation?.stateVersion).toBe(5n);
    // The snapshot must not advance the cursor, so replay (9) and live (11)
    // entries on both sides of the boundary survive, in sequence order.
    expect(store.entries.map((e) => e.sequence)).toEqual([9n, 11n]);
    expect(store.latestSequence).toBe(11n);
    expect(store.streamStatus).toBe('connected');
  });

  it('AC-01: out-of-order entries never regress the latest sequence', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(
      snapshot('op-1', 1n, 10n),
      entry('op-1', 5n),
      entry('op-1', 3n),
      entry('op-1', 7n),
    ));

    await store.load('op-1');
    await flush();

    expect(store.entries.map((e) => e.sequence)).toEqual([5n, 7n]);
    expect(store.latestSequence).toBe(7n);
  });

  it('AC-09: entries sharing an operation_state_version with distinct sequences are all kept', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(
      snapshot('op-1', 1n, 10n),
      entry('op-1', 11n, { id: 'a-1', operationStateVersion: 4n, kind: TimelineEntryKind.ACK }),
      entry('op-1', 12n, { id: 'a-2', operationStateVersion: 4n, kind: TimelineEntryKind.ROLLOUT_PROGRESS }),
      entry('op-1', 13n, { id: 'a-3', operationStateVersion: 4n, kind: TimelineEntryKind.ERROR }),
    ));

    await store.load('op-1');
    await flush();

    expect(store.entries).toHaveLength(3);
    expect(store.entries.map((e) => e.kind)).toEqual(['ACK', 'ROLLOUT_PROGRESS', 'ERROR']);
  });

  it('AC-14: caps history at 500 entries and flags truncation', async () => {
    const store = setupStore();
    const messages: WatchOperationResponse[] = [snapshot('op-1', 1n, 1n)];
    for (let i = 2n; i <= 552n; i++) {
      messages.push(entry('op-1', i, { id: `bulk-${i}` }));
    }
    mockedWatch.mockResolvedValue(streamOf(...messages));

    await store.load('op-1');
    await flush();

    expect(store.entries).toHaveLength(500);
    expect(store.historyTruncated).toBe(true);
    expect(store.entries[0]!.sequence).toBe(53n);
    expect(store.latestSequence).toBe(552n);
  });

  it('AC-05: 30s without messages disconnects and a successful reconnect clears the banner before polling', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 1n, 3n)));
    mockedGet.mockResolvedValue({ operationId: 'op-1', state: 'running', stateVersion: 1n } as never);

    await store.load('op-1');
    await flush();
    expect(store.streamStatus).toBe('connected');

    await vi.advanceTimersByTimeAsync(30_000);
    await flush();
    expect(store.streamStatus).toBe('disconnected');

    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 2n, 4n)));
    await vi.advanceTimersByTimeAsync(1_000);
    await flush();

    expect(store.streamStatus).toBe('connected');
    expect(mockedGet).not.toHaveBeenCalled();
  });

  it('AC-18: disconnection keeps the last authoritative operation and entries', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 1n, 3n), entry('op-1', 4n)));

    await store.load('op-1');
    await flush();
    await vi.advanceTimersByTimeAsync(30_000);
    await flush();

    expect(store.operation?.state).toBe('running');
    expect(store.entries).toHaveLength(1);
  });

  it('AC-11: reconnect resumes from the latest accepted sequence', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 1n, 3n), entry('op-1', 4n)));

    await store.load('op-1');
    await flush();
    await vi.advanceTimersByTimeAsync(30_000);
    await flush();

    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 2n, 4n)));
    await vi.advanceTimersByTimeAsync(1_000);
    await flush();

    expect(mockedWatch.mock.calls[1]![0]).toBe('op-1');
    expect(mockedWatch.mock.calls[1]![1]).toBe(4n);
  });

  it('AC-12: cursor_expired clears entries, sets historyGap, rebuilds with carried snapshot sequence', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 1n, 3n), entry('op-1', 4n)));
    await store.load('op-1');
    await flush();

    mockedWatch.mockRejectedValueOnce(cursorExpiredError(42n));
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 5n, 42n)));

    await vi.advanceTimersByTimeAsync(30_000); // disconnect
    await vi.advanceTimersByTimeAsync(1_000); // reconnect hits cursor_expired
    await flush();

    expect(store.entries).toHaveLength(0);
    // Rebuild uses the snapshot sequence carried by the error, then the
    // re-established stream's snapshot is the new authority. The rebuilt
    // stream delivering its snapshot clears the gap (AC-057-29).
    expect(mockedWatch.mock.calls[2]![1]).toBe(42n);
    expect(store.historyGap).toBe(false);
    expect(store.streamStatus).toBe('connected');
    expect(store.operation?.stateVersion).toBe(5n);
  });

  it('AC-12b: cursor_expired with X-Snapshot-Proto rebuilds from the decoded snapshot', async () => {
    const store = setupStore();
    const decodeSnapshotProto = vi.fn(() => ({
      operation: {
        operationId: 'op-1',
        releaseDefinitionId: 'def-1',
        operationType: 'EMERGENCY' as const,
        state: 'cancelled' as const,
        stateVersion: 7n,
        bundleId: '',
        valuesRevisionId: '',
        expectedRevision: 0,
        targetRevision: 0,
        createdBy: '',
        createdAt: null,
        updatedAt: null,
        terminalAt: null,
        deadline: null,
        lastError: '',
        effectStatus: 'UNKNOWN' as const,
      },
      snapshotSequence: 99n,
      retainedFromSequence: 20n,
    }));
    store.configure({ decodeSnapshotProto });
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 1n, 3n), entry('op-1', 4n)));
    await store.load('op-1');
    await flush();

    mockedWatch.mockRejectedValueOnce(cursorExpiredError(42n, 'encoded'));
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 7n, 99n)));

    await vi.advanceTimersByTimeAsync(30_000); // disconnect
    await vi.advanceTimersByTimeAsync(1_000); // reconnect hits cursor_expired
    await flush();

    // The X-Snapshot-Proto header is decoded, not just mirrored as a string.
    expect(decodeSnapshotProto).toHaveBeenCalledWith('encoded');
    // The decoded snapshot is the rebuild authority: its snapshot sequence
    // drives the re-established stream, not the metadata header's (42n).
    expect(mockedWatch.mock.calls[2]![1]).toBe(99n);
    expect(store.operation?.stateVersion).toBe(7n);
    expect(store.historyGap).toBe(false);
    expect(store.streamStatus).toBe('connected');
  });

  it('AC-29: cursor rebuild failure keeps gap and empties entries while polling', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 1n, 3n)));
    await store.load('op-1');
    await flush();

    mockedWatch.mockRejectedValueOnce(cursorExpiredError(42n));
    mockedWatch.mockRejectedValueOnce(new ConnectError('down', Code.Unavailable));
    mockedGet.mockResolvedValue({ operationId: 'op-1', state: 'running', stateVersion: 1n } as never);

    await vi.advanceTimersByTimeAsync(30_000); // heartbeat timeout → disconnected
    await vi.advanceTimersByTimeAsync(1_000); // reconnect#1 → cursor_expired → rebuild fails
    await flush();

    expect(store.historyGap).toBe(true);
    expect(store.entries).toHaveLength(0);
    expect(store.streamStatus).toBe('disconnected');

    await vi.advanceTimersByTimeAsync(5_000); // polling (re)starts while degraded
    await flush();
    expect(mockedGet).toHaveBeenCalled();
  });

  it('AC-29b: a later successful reconnect clears historyGap', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 1n, 3n)));
    await store.load('op-1');
    await flush();

    mockedWatch.mockRejectedValueOnce(cursorExpiredError(42n));
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 5n, 42n)));

    await vi.advanceTimersByTimeAsync(30_000); // disconnect
    await vi.advanceTimersByTimeAsync(1_000); // reconnect → cursor_expired → rebuild succeeds
    await flush();

    expect(store.historyGap).toBe(false);
    expect(store.streamStatus).toBe('connected');
    expect(store.operation?.stateVersion).toBe(5n);
  });

  it('AC-25/28: first-load failure shows retryable error; retry replays Watch(0); not_found is not retryable', async () => {
    const store = setupStore();
    mockedWatch.mockRejectedValueOnce(new ConnectError('down', Code.Unavailable));
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 1n, 3n)));

    await store.load('op-1');
    await flush();
    expect(store.initialError?.retryable).toBe(true);
    expect(store.operation).toBeNull();
    expect(store.streamStatus).toBe('connecting');

    store.retryInitial();
    await flush();
    expect(store.operation).not.toBeNull();
    expect(mockedWatch.mock.calls[1]![1]).toBe(0n);

    const store2 = setupStore();
    mockedWatch.mockRejectedValueOnce(new ConnectError('missing', Code.NotFound));
    await store2.load('op-2');
    await flush();
    expect(store2.initialError?.retryable).toBe(false);
  });

  it('AC-02: terminal operations disable cancel', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n, { state: OperationStatus.SUCCEEDED })));
    await store.load('op-1');
    await flush();

    expect(store.canCancel).toBe(false);
    expect(store.isTerminal).toBe(true);
  });

  it('AC-07: viewer role hides cancel entirely (active and disabled affordances)', async () => {
    const store = setupStore();
    store.configure({ canWrite: () => false });
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n)));
    await store.load('op-1');
    await flush();

    expect(store.showCancel).toBe(false);
    expect(store.canCancel).toBe(false);

    // Even a terminal operation must not surface any cancel UI for viewers.
    const terminal = setupStore();
    terminal.configure({ canWrite: () => false });
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-2', 1n, 3n, { state: OperationStatus.SUCCEEDED })));
    await terminal.load('op-2');
    await flush();
    expect(terminal.isTerminal).toBe(true);
    expect(terminal.showCancel).toBe(false);
  });

  it('server permission_denied hides cancel even with local write role', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n)));
    mockedCancel.mockRejectedValueOnce(new ConnectError('forbidden', Code.PermissionDenied));
    await store.load('op-1');
    await flush();

    expect(store.showCancel).toBe(true);
    await store.submitCancel('取消');
    expect(store.showCancel).toBe(false);
    expect(store.canCancel).toBe(false);
  });

  it('AC-20: EMERGENCY running hides cancel, queued allows it', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n, {
      operationType: 'EMERGENCY', state: OperationStatus.RUNNING,
    })));
    await store.load('op-1');
    await flush();
    expect(store.canCancel).toBe(false);

    const store2 = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-2', 1n, 3n, {
      operationType: 'EMERGENCY', state: OperationStatus.QUEUED,
    })));
    await store2.load('op-2');
    await flush();
    expect(store2.canCancel).toBe(true);
  });

  it('AC-13: a second submit while cancelling sends no new request', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n)));
    await store.load('op-1');
    await flush();

    const { promise: cancelPromise, resolve: resolveCancel } = Promise.withResolvers<unknown>();
    mockedCancel.mockReturnValueOnce(cancelPromise as never);

    const first = store.submitCancel('因为测试');
    const second = store.submitCancel('因为测试');
    expect(store.cancelLoading).toBe(true);
    expect(mockedCancel).toHaveBeenCalledTimes(1);

    resolveCancel?.({ operation: {}, requestId: 'req-1' } as never);
    await first;
    await second;
    expect(mockedCancel).toHaveBeenCalledTimes(1);
  });

  it('AC-06: network retry reuses the same Idempotency-Key', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n)));
    await store.load('op-1');
    await flush();

    mockedCancel.mockRejectedValueOnce(new ConnectError('down', Code.Unavailable));
    mockedCancel.mockResolvedValueOnce({ operation: {}, requestId: 'req-1' } as never);

    const first = await store.submitCancel('重试幂等');
    expect(first.ok).toBe(false);
    const second = await store.submitCancel('重试幂等');
    expect(second.ok).toBe(true);

    const keys = mockedCancel.mock.calls.map((call) => (call[0] as { idempotencyKey: string }).idempotencyKey);
    expect(keys[0]).toBe(keys[1]);
    expect(keys[0]).toBeTruthy();
  });

  it('AC-17: blank or over-length reasons are rejected client-side', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n)));
    mockedCancel.mockResolvedValue({ operation: {}, requestId: 'req-1' } as never);
    await store.load('op-1');
    await flush();

    const blank = await store.submitCancel('   ');
    expect(blank.errorCode).toBe('invalid_argument');
    expect(mockedCancel).not.toHaveBeenCalled();

    const tooLong = await store.submitCancel('字'.repeat(501));
    expect(tooLong.errorCode).toBe('invalid_argument');
    expect(mockedCancel).not.toHaveBeenCalled();

    const ok = await store.submitCancel('业务原因');
    expect(ok.errorCode).toBeNull();
  });

  it('AC-03: optimistic_lock_conflict auto-refreshes GetOperation', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n)));
    mockedCancel.mockRejectedValueOnce(new ConnectError('conflict', Code.Aborted, { 'X-Reason-Code': 'optimistic_lock_conflict' }));
    mockedGet.mockResolvedValue({ operationId: 'op-1', state: 'running', stateVersion: 2n } as never);

    await store.load('op-1');
    await flush();
    const result = await store.submitCancel('冲突');
    await flush();

    expect(result.errorCode).toBe('optimistic_lock_conflict');
    expect(mockedGet).toHaveBeenCalledTimes(1);
    expect(store.operation?.stateVersion).toBe(2n);
  });

  it('AC-05/24: the 5s poll keeps repeating while disconnected', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 1n, 3n)));
    mockedWatch.mockRejectedValue(new ConnectError('down', Code.Unavailable));
    mockedGet.mockResolvedValue({ operationId: 'op-1', state: 'running', stateVersion: 1n } as never);
    await store.load('op-1');
    await flush();

    await vi.advanceTimersByTimeAsync(30_000); // heartbeat timeout → disconnected
    await flush();
    expect(mockedGet).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(5_000);
    await flush();
    await vi.advanceTimersByTimeAsync(5_000);
    await flush();
    expect(mockedGet.mock.calls.length).toBeGreaterThanOrEqual(2);
  });

  it('AC-24: poll responses never regress state_version and produce no entries', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 5n, 3n)));
    mockedGet.mockResolvedValue({ operationId: 'op-1', state: 'running', stateVersion: 3n } as never);
    await store.load('op-1');
    await flush();

    await vi.advanceTimersByTimeAsync(30_000);
    await vi.advanceTimersByTimeAsync(5_000);
    await flush();

    expect(store.operation?.stateVersion).toBe(5n);
    expect(store.entries).toHaveLength(0);
  });

  it('AC-19: EMERGENCY terminal + UNKNOWN watches, EMERGENCY_EFFECT_RESOLVED resolves', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n, {
      operationType: 'EMERGENCY',
      state: OperationStatus.CANCELLED,
      effectStatus: EmergencyEffectStatus.UNKNOWN,
    })));
    await store.load('op-1');
    await flush();

    expect(store.emergencyEffectStatus).toBe('watching');

    mockedWatch.mockResolvedValueOnce(streamOf(entry('op-1', 4n, {
      id: 'resolved-1',
      kind: TimelineEntryKind.EMERGENCY_EFFECT_RESOLVED,
      fromState: 'UNKNOWN',
      toState: 'APPLIED',
    })));
    await vi.advanceTimersByTimeAsync(30_000);
    await vi.advanceTimersByTimeAsync(1_000);
    await flush();

    expect(store.emergencyEffectStatus).toBe('resolved');
  });

  it('AC-21: non-EMERGENCY and NOT_STARTED stay not_applicable', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n, { state: OperationStatus.SUCCEEDED })));
    await store.load('op-1');
    await flush();
    expect(store.emergencyEffectStatus).toBe('not_applicable');

    const store2 = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n, {
      operationType: 'EMERGENCY',
      state: OperationStatus.CANCELLED,
      effectStatus: EmergencyEffectStatus.NOT_STARTED,
    })));
    await store2.load('op-1');
    await flush();
    expect(store2.emergencyEffectStatus).toBe('not_applicable');
  });

  it('AC-16: live updates flag off creates no stream, no auto-polling, and no implicit fetch', async () => {
    const store = useOperationTimelineStore();
    store.configure({ liveUpdatesEnabled: () => false, canWrite: () => true });
    mockedGet.mockResolvedValue({ operationId: 'op-1', state: 'running', stateVersion: 1n } as never);

    await store.load('op-1');
    await flush();

    expect(mockedWatch).not.toHaveBeenCalled();
    expect(mockedGet).not.toHaveBeenCalled();
    expect(store.streamStatus).toBe('connecting');

    await vi.advanceTimersByTimeAsync(60_000);
    await flush();
    expect(mockedGet).not.toHaveBeenCalled();

    // Only the explicit refresh action loads data (AC-057-16).
    await store.refresh();
    expect(mockedGet).toHaveBeenCalledTimes(1);
    expect(store.operation?.stateVersion).toBe(1n);
  });

  it('AC-15: switching scopes aborts the old stream and loads the new one', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-A', 1n, 3n)));
    await store.load('op-A');
    await flush();

    const abortSpy = vi.spyOn(AbortController.prototype, 'abort');
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-B', 1n, 1n)));
    await store.load('op-B');
    await flush();

    expect(abortSpy).toHaveBeenCalled();
    expect(store.operation?.operationId).toBe('op-B');
  });

  it('cancel success resets loading, replaces operation, and closes the dialog', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n)));
    mockedCancel.mockResolvedValue({
      operation: { ...store.operation, state: 'cancelling' },
      requestId: 'req-1',
    } as never);
    await store.load('op-1');
    await flush();

    store.cancelDialogOpen = true;
    const result = await store.submitCancel('业务原因');
    expect(result.ok).toBe(true);
    expect(store.cancelLoading).toBe(false);
    expect(store.cancelDialogOpen).toBe(false);
    expect(store.operation?.state).toBe('cancelling');
    expect(store.activeCancelIdempotencyKey).toBeNull();
  });

  it('AC-22: cancel_not_allowed keeps the dialog open, refreshes GetOperation, and re-enables submit', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n)));
    mockedCancel.mockRejectedValueOnce(
      new ConnectError('not allowed', Code.FailedPrecondition, { 'X-Reason-Code': 'cancel_not_allowed' }),
    );
    mockedGet.mockResolvedValue({ operationId: 'op-1', state: 'failed', stateVersion: 2n } as never);
    await store.load('op-1');
    await flush();
    store.cancelDialogOpen = true;

    const result = await store.submitCancel('取消');
    await flush();

    expect(result.errorCode).toBe('cancel_not_allowed');
    // AC-22: only a success or an explicit user close may close the dialog.
    expect(store.cancelDialogOpen).toBe(true);
    expect(mockedGet).toHaveBeenCalledTimes(1);
    expect(store.operation?.stateVersion).toBe(2n);
    expect(store.cancelError?.code).toBe('cancel_not_allowed');
    // Submit re-arms after a rejection; the button becomes clickable again.
    expect(store.cancelLoading).toBe(false);
    expect(store.activeCancelIdempotencyKey).toBeNull();
  });

  it('AC-19/20: resolved is sticky against later non-resolved entries', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n, {
      operationType: 'EMERGENCY',
      state: OperationStatus.CANCELLED,
      effectStatus: EmergencyEffectStatus.UNKNOWN,
    })));
    await store.load('op-1');
    await flush();
    expect(store.emergencyEffectStatus).toBe('watching');

    // Reconnect so the stream delivers the resolution entry, then more entries.
    mockedWatch.mockResolvedValueOnce(streamOf(
      entry('op-1', 4n, { id: 'resolved-1', kind: TimelineEntryKind.EMERGENCY_EFFECT_RESOLVED, fromState: 'UNKNOWN', toState: 'APPLIED' }),
      entry('op-1', 5n, { id: 'later-1' }),
      entry('op-1', 6n, { id: 'later-2' }),
    ));
    await vi.advanceTimersByTimeAsync(30_000);
    await vi.advanceTimersByTimeAsync(1_000);
    await flush();

    expect(store.emergencyEffectStatus).toBe('resolved');
  });

  it('same-id reload: stale stream entries never write into the new scope', async () => {
    const store = setupStore();
    let releaseOld: () => void = () => {};
    const oldStream = (async function* () {
      yield snapshot('op-1', 1n, 3n);
      // Hold the stream open; the old scope must not deliver after reload.
      await new Promise<void>((resolve) => { releaseOld = resolve; });
      yield entry('op-1', 4n, { id: 'stale-entry' });
    })();
    mockedWatch.mockResolvedValueOnce(oldStream as never);
    await store.load('op-1');
    await flush();
    expect(store.operation?.stateVersion).toBe(1n);

    // Same-id remount: reset + load opens a new scope for a new stream.
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 9n, 10n)));
    store.reset();
    await store.load('op-1');
    await flush();
    expect(store.operation?.stateVersion).toBe(9n);

    // Old scope finally delivers: must be dropped.
    releaseOld();
    await flush();
    expect(store.entries).toHaveLength(0);
    expect(store.operation?.stateVersion).toBe(9n);
  });

  it('scope switch: a stale cancel response never clears the new scope loading or writes state', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n)));
    await store.load('op-1');
    await flush();

    let rejectOld: ((e: unknown) => void) | undefined;
    const oldCancel = new Promise<never>((_, reject) => { rejectOld = reject; });
    mockedCancel.mockReturnValueOnce(oldCancel as never);
    void store.submitCancel('旧请求');
    expect(store.cancelLoading).toBe(true);

    // Navigate away mid-flight: the new scope reinitializes cancel state.
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-2', 1n, 3n)));
    await store.load('op-2');
    await flush();
    expect(store.cancelLoading).toBe(false);

    // New scope starts its own cancel, held open so we can observe whether
    // the stale old-scope rejection touches it.
    let resolveNew: ((v: never) => void) | undefined;
    const newCancel = new Promise<never>((resolve) => { resolveNew = resolve; });
    mockedCancel.mockReturnValueOnce(newCancel as never);
    const second = store.submitCancel('新请求');
    expect(store.cancelLoading).toBe(true);
    expect(mockedCancel).toHaveBeenCalledTimes(2);

    // Stale old-scope rejection arrives: must not clear the new loading.
    rejectOld!(new ConnectError('down', Code.Unavailable));
    await flush();
    expect(store.cancelLoading).toBe(true); // new scope cancel still in flight
    expect(store.cancelError).toBeNull();  // old failure must not surface

    // New scope cancel completes normally.
    resolveNew!({ operation: { operationId: 'op-2', state: 'cancelling', stateVersion: 2n }, requestId: 'req-2' } as never);
    await flush();
    expect((await second).ok).toBe(true);
    expect(store.cancelLoading).toBe(false);
    expect(store.operation?.operationId).toBe('op-2');
  });

  it('live: STATE_TRANSITION merges the authoritative state into the operation summary', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(
      snapshot('op-1', 1n, 10n),
      entry('op-1', 11n, { operationStateVersion: 2n, kind: TimelineEntryKind.STATE_TRANSITION, toState: 'cancelling' }),
    ));

    await store.load('op-1');
    await flush();

    expect(store.operation?.state).toBe('cancelling');
    expect(store.operation?.stateVersion).toBe(2n);
    // cancelling is not cancellable: the entry disappears right after merge.
    expect(store.canCancel).toBe(false);
  });

  it('AC-01: out-of-order STATE_TRANSITION never regresses the summary', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(
      snapshot('op-1', 1n, 10n),
      entry('op-1', 11n, { operationStateVersion: 5n, kind: TimelineEntryKind.STATE_TRANSITION, toState: 'cancelled' }),
      entry('op-1', 12n, { operationStateVersion: 3n, kind: TimelineEntryKind.STATE_TRANSITION, toState: 'running' }),
    ));

    await store.load('op-1');
    await flush();

    expect(store.operation?.state).toBe('cancelled');
    expect(store.operation?.stateVersion).toBe(5n);
    expect(store.latestSequence).toBe(12n);
  });

  it('AC-06: a business rejection drops the idempotency key so the next submit is a new intent', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n)));
    mockedCancel.mockRejectedValueOnce(new ConnectError('conflict', Code.Aborted, { 'X-Reason-Code': 'optimistic_lock_conflict' }));
    mockedCancel.mockResolvedValue({ operation: { operationId: 'op-1', state: 'cancelling', stateVersion: 2n }, requestId: 'req-1' } as never);
    mockedGet.mockResolvedValue({ operationId: 'op-1', state: 'running', stateVersion: 2n } as never);

    await store.load('op-1');
    await flush();
    const first = await store.submitCancel('冲突');
    await flush();

    expect(first.errorCode).toBe('optimistic_lock_conflict');
    expect(store.operation?.stateVersion).toBe(2n);

    const second = await store.submitCancel('再试');
    expect(second.errorCode).toBeNull();
    const firstKey = mockedCancel.mock.calls[0]![0].idempotencyKey;
    const secondKey = mockedCancel.mock.calls[1]![0].idempotencyKey;
    expect(firstKey).not.toBe(secondKey);
  });

  it('AC-06: a transient network error keeps the idempotency key for retry', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(snapshot('op-1', 1n, 3n)));
    mockedCancel.mockRejectedValueOnce(new ConnectError('down', Code.Unavailable));
    mockedCancel.mockResolvedValue({ operation: { operationId: 'op-1', state: 'cancelling', stateVersion: 2n }, requestId: 'req-1' } as never);

    await store.load('op-1');
    await flush();
    const first = await store.submitCancel('网络抖动');
    expect(first.errorCode).toBe('network_error');

    const second = await store.submitCancel('网络抖动');
    expect(second.errorCode).toBeNull();
    expect(mockedCancel.mock.calls[0]![0].idempotencyKey).toBe(mockedCancel.mock.calls[1]![0].idempotencyKey);
  });

  it('AC-19/20: EMERGENCY_EFFECT_RESOLVED merges the authoritative effect outcome', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(
      snapshot('op-1', 4n, 3n, { operationType: 'EMERGENCY', state: OperationStatus.CANCELLED, effectStatus: EmergencyEffectStatus.UNKNOWN }),
      entry('op-1', 4n, {
        id: 'resolved-1',
        operationStateVersion: 5n,
        kind: TimelineEntryKind.EMERGENCY_EFFECT_RESOLVED,
        effectFrom: 'UNKNOWN',
        effectTo: 'APPLIED',
      }),
    ));

    await store.load('op-1');
    await flush();

    expect(store.emergencyEffectStatus).toBe('resolved');
    expect(store.operation?.effectStatus).toBe('APPLIED');
    expect(store.operation?.stateVersion).toBe(5n);
  });

  it('AC-01: stale EMERGENCY_EFFECT_RESOLVED never regresses the summary', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(streamOf(
      snapshot('op-1', 6n, 3n, { operationType: 'EMERGENCY', state: OperationStatus.CANCELLED, effectStatus: EmergencyEffectStatus.APPLIED }),
      entry('op-1', 4n, {
        id: 'resolved-1',
        operationStateVersion: 5n,
        kind: TimelineEntryKind.EMERGENCY_EFFECT_RESOLVED,
        effectFrom: 'UNKNOWN',
        effectTo: 'NOT_APPLIED',
      }),
    ));

    await store.load('op-1');
    await flush();

    // Snapshot already carries v6/APPLIED; the stale resolved entry must not
    // regress either the effect or the version.
    expect(store.operation?.effectStatus).toBe('APPLIED');
    expect(store.operation?.stateVersion).toBe(6n);
    expect(store.emergencyEffectStatus).toBe('resolved');
  });

  it('AC-05/18: normal stream EOF falls back to disconnected and keeps the data', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValue(eofStreamOf(
      snapshot('op-1', 1n, 3n),
      entry('op-1', 4n, { operationStateVersion: 2n, kind: TimelineEntryKind.STATE_TRANSITION, toState: 'cancelling' }),
    ));

    await store.load('op-1');
    await flush();

    // The finite stream ended: never stranded in 'connected'.
    expect(store.streamStatus).toBe('disconnected');
    expect(store.operation?.state).toBe('cancelling');
    expect(store.entries).toHaveLength(1);
  });

  it('AC-05/11: EOF degraded mode reconnects with bounded backoff and stops polling', async () => {
    const store = setupStore();
    mockedWatch.mockResolvedValueOnce(eofStreamOf(snapshot('op-1', 1n, 3n)));
    mockedWatch.mockResolvedValueOnce(streamOf(snapshot('op-1', 2n, 5n)));
    mockedGet.mockResolvedValue({ operationId: 'op-1', state: 'running', stateVersion: 1n } as never);

    await store.load('op-1');
    await flush();
    expect(store.streamStatus).toBe('disconnected');

    await vi.advanceTimersByTimeAsync(1_000); // reconnect#1 (base 1s, jitter 0)
    await flush();
    expect(store.streamStatus).toBe('connected');
    expect(store.operation?.stateVersion).toBe(2n);
    expect(mockedWatch).toHaveBeenCalledTimes(2);

    // Polling must not run after reconnect (no GetOperation calls).
    await vi.advanceTimersByTimeAsync(10_000);
    expect(mockedGet).not.toHaveBeenCalled();
  });
});
