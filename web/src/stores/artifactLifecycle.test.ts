import { createPinia, setActivePinia } from 'pinia';
import { Code, ConnectError } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useArtifactLifecycleStore } from './artifactLifecycle';
import * as api from '@/connect/cleanup-api';

vi.mock('@/connect/cleanup-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, runCleanup: vi.fn(), unarchiveBundle: vi.fn() };
});

const mockedRun = vi.mocked(api.runCleanup);
const mockedUnarchive = vi.mocked(api.unarchiveBundle);

function result(overrides: Partial<api.CleanupResult> = {}): api.CleanupResult {
  return { deletedBundles: 3, deletedCandidates: 1, deletedPreflights: 2, skippedBundles: 0, errors: [], ...overrides };
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedRun.mockReset().mockResolvedValue(result());
  mockedUnarchive.mockReset().mockResolvedValue({ bundleId: 'b1', previousStatus: 'validated' });
});

describe('artifact lifecycle store', () => {
  it('keeps the counters from a clean run', async () => {
    const store = useArtifactLifecycleStore();

    const ok = await store.clean('key-1');

    expect(ok).toBe(true);
    expect(mockedRun).toHaveBeenCalledWith('key-1');
    expect(store.result?.deletedBundles).toBe(3);
    expect(store.hasProblems).toBe(false);
    expect(store.notice).toBe('清理已完成');
  });

  // The contract: a SUCCESSFUL run can still report per-phase problems, so the notice
  // must not claim a clean sweep.
  it('does not claim a clean sweep when the run reported non-fatal errors', async () => {
    mockedRun.mockResolvedValue(result({ deletedBundles: 0, errors: ['preflight phase: timeout'] }));
    const store = useArtifactLifecycleStore();

    await store.clean('key-2');

    expect(store.hasProblems).toBe(true);
    expect(store.notice).toContain('非致命错误');
    expect(store.result?.errors).toEqual(['preflight phase: timeout']);
  });

  it('reports a repeated idempotency key instead of pretending to re-run', async () => {
    mockedRun.mockRejectedValue(new ConnectError('cleanup_already_requested', Code.AlreadyExists));
    const store = useArtifactLifecycleStore();

    const ok = await store.clean('key-3');

    expect(ok).toBe(false);
    expect(store.failure?.code).toBe('already_requested');
    expect(store.result).toBeNull();
  });

  it('restores an archived bundle and reports the stored origin status', async () => {
    const store = useArtifactLifecycleStore();

    const ok = await store.restore('b1');

    expect(ok).toBe(true);
    expect(mockedUnarchive).toHaveBeenCalledWith('b1');
    expect(store.restoreResult?.previousStatus).toBe('validated');
    expect(store.notice).toContain('validated');
  });

  it('reports an unrestorable bundle', async () => {
    mockedUnarchive.mockRejectedValue(new ConnectError('unrestorable', Code.FailedPrecondition));
    const store = useArtifactLifecycleStore();

    await store.restore('b2');

    expect(store.restoreFailure?.code).toBe('unrestorable');
    expect(store.restoreResult).toBeNull();
  });
});
