import { createPinia, setActivePinia } from 'pinia';
import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it, vi } from 'vitest';
import { useConvergenceSelectionStore } from '@/stores/convergenceSelection';
import type { ConvergenceTaskDisplay } from '@/features/emergency/model';

function task(taskId: string, partial: Partial<ConvergenceTaskDisplay> = {}): ConvergenceTaskDisplay {
  return {
    taskId,
    operationId: 'op',
    opType: 'SET_CONTAINER_IMAGE',
    targetSummary: 'DEPLOYMENT/api, container=app',
    submittedAt: null,
    reasonDisplay: 'reason',
    promotionPaths: ['image.app'],
    activeRevisionId: '',
    activeRevisionStatus: '',
    selectable: true,
    incompatibilityReason: '',
    ...partial,
  };
}

describe('convergenceSelection store (AC-058-34/42)', () => {
  it('loads tasks and enforces server selectability in toggleTask', async () => {
    setActivePinia(createPinia());
    const store = useConvergenceSelectionStore();
    store.configure({
      loadTasks: async () => [
        task('t1'),
        task('t2', { selectable: false, incompatibilityReason: 'bound' }),
        task('t3', { activeRevisionId: 'r1' }),
      ],
    });

    await store.load('def1');
    expect(store.tasks).toHaveLength(3);
    store.toggleTask('t1');
    store.toggleTask('t2');
    store.toggleTask('t3');
    expect(store.selectedTaskIds).toEqual(['t1']);
  });

  it('derives compatibility conflicts and blocks prepare (AC-058-34)', async () => {
    setActivePinia(createPinia());
    const store = useConvergenceSelectionStore();
    store.configure({
      loadTasks: async () => [
        task('t1', { promotionPaths: ['image.app'] }),
        task('t2', { promotionPaths: ['image.app'] }),
      ],
    });

    await store.load('def1');
    store.toggleTask('t1');
    store.toggleTask('t2');
    expect(store.compatibility.valid).toBe(false);
    expect(store.canPrepare).toBe(false);
    await expect(store.prepare(0n)).resolves.toBeNull();
  });

  it('prepares a compatible selection and stores the session token', async () => {
    setActivePinia(createPinia());
    const store = useConvergenceSelectionStore();
    const prepare = vi.fn().mockResolvedValue({
      prepareToken: 'token-1',
      expiresAt: '2026-08-22T10:00:00.000Z',
      parentRevisionId: '',
      parentVersion: 0n,
      lockedPaths: ['image.app'],
      conflictTaskIds: [],
    });
    store.configure({
      loadTasks: async () => [task('t1', { promotionPaths: ['image.app'] })],
      prepare,
    });

    await store.load('def1');
    store.toggleTask('t1');
    const session = await store.prepare(0n);
    expect(session?.prepareToken).toBe('token-1');
    expect(store.preparedSession?.lockedPaths).toEqual(['image.app']);
    expect(prepare).toHaveBeenCalledWith(
      { releaseDefinitionId: 'def1', taskIds: ['t1'], expectedParentVersion: 0n },
      expect.anything(),
    );
  });

  it('surfaces conflictTaskIds from a Prepare race (AC-058-34)', async () => {
    setActivePinia(createPinia());
    const store = useConvergenceSelectionStore();
    store.configure({
      loadTasks: async () => [task('t1')],
      prepare: async () => ({
        prepareToken: '',
        expiresAt: null,
        parentRevisionId: '',
        parentVersion: 0n,
        lockedPaths: [],
        conflictTaskIds: ['t9'],
      }),
    });

    await store.load('def1');
    store.toggleTask('t1');
    const session = await store.prepare(0n);
    expect(session).toBeNull();
    expect(store.conflictTaskIds).toEqual(['t9']);
    expect(store.prepareError?.code).toBe('convergence_conflict');
  });

  it('maps server errors and rejects over-cap selections (AC-058-42)', async () => {
    setActivePinia(createPinia());
    const store = useConvergenceSelectionStore();
    store.configure({
      loadTasks: async () => Array.from({ length: 51 }, (_, i) => task(`t${i}`, { promotionPaths: [`p${i}`] })),
    });

    await store.load('def1');
    for (let i = 0; i < 51; i++) store.toggleTask(`t${i}`);
    expect(store.selectedTaskIds).toHaveLength(51);
    expect(store.compatibility.valid).toBe(false);
    expect(store.canPrepare).toBe(false);
  });

  it('maps prepare server errors through the typed error decoder', async () => {
    setActivePinia(createPinia());
    const store = useConvergenceSelectionStore();
    store.configure({
      loadTasks: async () => [task('t1')],
      prepare: vi.fn().mockRejectedValue(new ConnectError('expired', Code.FailedPrecondition, new Headers({ 'X-Reason-Code': 'prepare_token_expired' }))),
    });

    await store.load('def1');
    store.toggleTask('t1');
    const session = await store.prepare(0n);
    expect(session).toBeNull();
    expect(store.prepareError).toMatchObject({ code: 'prepare_token_expired', retryable: true });
  });
});
