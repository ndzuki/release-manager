import { createPinia, setActivePinia } from 'pinia';
import { Code, ConnectError } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useStuckLocksStore } from './stuckLocks';
import * as api from '@/connect/stuck-lock-api';

vi.mock('@/connect/stuck-lock-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, listStuckLocks: vi.fn(), releaseEmergencyLock: vi.fn() };
});

const mockedList = vi.mocked(api.listStuckLocks);
const mockedRelease = vi.mocked(api.releaseEmergencyLock);

function lock(intentId: string): api.StuckLockView {
  return {
    intentId,
    operationId: `op-${intentId}`,
    releaseDefinitionId: 'def-1',
    action: '设置副本数',
    lockPathSummary: 'Deployment/api, container=app',
    terminalAt: '2026-09-28T10:00:00Z',
    stuckSince: '2026-09-28T10:05:00Z',
    observeTimeoutDisplay: '5m0s',
  };
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedList.mockReset().mockResolvedValue([lock('intent-1')]);
  mockedRelease.mockReset().mockResolvedValue({ intentId: 'intent-1', lockReleased: true, effectStatus: 'NOT_APPLIED', auditEventId: 'audit-1' });
});

describe('stuck locks store', () => {
  it('lists every stuck lock in scope when no definition is given', async () => {
    const store = useStuckLocksStore();

    await store.load('');

    expect(mockedList).toHaveBeenCalledWith('');
    expect(store.locks.map((l) => l.intentId)).toEqual(['intent-1']);
  });

  it('passes the definition filter through, trimmed', async () => {
    const store = useStuckLocksStore();

    await store.load('  def-9  ');

    expect(mockedList).toHaveBeenCalledWith('def-9');
  });

  // "No stuck locks" is the expected steady state, not a failure.
  it('treats an empty list as the expected state', async () => {
    mockedList.mockResolvedValue([]);
    const store = useStuckLocksStore();

    await store.load('');

    expect(store.isEmpty).toBe(true);
    expect(store.failure).toBeNull();
  });

  it('releases a lock with the chosen mode, reason and evidence, then reloads', async () => {
    const store = useStuckLocksStore();
    await store.load('');
    mockedList.mockClear();

    const ok = await store.release({
      intentId: 'intent-1',
      reason: 'deployment never ACKed',
      mode: 'NOT_APPLIED_PROVEN',
      evidence: 'kubectl shows old replicas',
    });

    expect(ok).toBe(true);
    expect(mockedRelease).toHaveBeenCalledWith({
      intentId: 'intent-1',
      reason: 'deployment never ACKed',
      mode: 'NOT_APPLIED_PROVEN',
      evidence: 'kubectl shows old replicas',
    });
    expect(mockedList).toHaveBeenCalledTimes(1);
    expect(store.notice).toContain('NOT_APPLIED');
    expect(store.lastRelease?.auditEventId).toBe('audit-1');
  });

  // Releasing is irreversible: a refusal must be reported against the list the
  // server actually holds (the reload must not wipe the message).
  it('reloads and still reports the refusal', async () => {
    const store = useStuckLocksStore();
    await store.load('');
    mockedList.mockClear();
    mockedRelease.mockRejectedValue(new ConnectError('intent not found', Code.NotFound, { 'X-Reason-Code': 'intent_not_found' }));

    const ok = await store.release({ intentId: 'intent-1', reason: 'r', mode: 'AUDITED_OVERRIDE' });

    expect(ok).toBe(false);
    expect(store.failure?.code).toBe('not_found');
    expect(store.failure?.typed).toBe(true);
    expect(mockedList).toHaveBeenCalledTimes(1);
  });

  it('reports a bad definition filter as a filter problem', async () => {
    mockedList.mockRejectedValue(new ConnectError('release definition not found', Code.NotFound));
    const store = useStuckLocksStore();

    await store.load('not-a-definition');

    expect(store.failure?.message).toContain('过滤条件');
    expect(store.failure?.message).not.toContain('已被释放');
  });

  it('lists an empty result when the list call itself fails', async () => {
    mockedList.mockRejectedValue(new ConnectError('boom', Code.Internal));
    const store = useStuckLocksStore();

    await store.load('');

    expect(store.locks).toEqual([]);
    expect(store.failure?.code).toBe('unavailable');
  });
});
