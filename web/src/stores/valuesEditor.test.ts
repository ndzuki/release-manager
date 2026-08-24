import { createPinia, setActivePinia } from 'pinia';
import { ConnectError, Code } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useValuesEditorStore } from './valuesEditor';
import type { ValuesRevision } from '@/types/valuesRevision';
import {
  approveValuesRevision,
  createValuesRevision,
  discardValuesRevision,
  listSecrets,
  listValuesRevisions,
  rejectValuesRevision,
  submitValuesRevision,
} from '@/connect/values-revision';
import { getPrepareSession } from '@/connect/emergency-api';

vi.mock('@/connect/values-revision', async (importOriginal) => {
  const original = await importOriginal<typeof import('@/connect/values-revision')>();
  return {
    ...original,
    createValuesRevision: vi.fn(),
    listSecrets: vi.fn(),
    listValuesRevisions: vi.fn(),
    submitValuesRevision: vi.fn(),
    approveValuesRevision: vi.fn(),
    rejectValuesRevision: vi.fn(),
    discardValuesRevision: vi.fn(),
  };
});

vi.mock('@/connect/emergency-api', async (importOriginal) => {
  const original = await importOriginal<typeof import('@/connect/emergency-api')>();
  return { ...original, getPrepareSession: vi.fn() };
});

const parent: ValuesRevision = {
  id: 'parent-1', releaseDefinitionId: 'definition-1', revision: 1, stateVersion: '3',
  document: '{"replicas":1}', valuesDigest: 'sha256:parent', status: 'approved', parentRevisionId: null,
  secretRefs: [], createdByUserId: 'creator-1', createdAt: '2026-07-23T00:00:00Z',
  convergenceTaskIds: [], lockedPaths: [],
};

const draft: ValuesRevision = {
  ...parent,
  id: 'draft-1', revision: 2, stateVersion: '1', document: '{"replicas":2}', valuesDigest: 'sha256:draft',
  status: 'draft', parentRevisionId: parent.id, createdByUserId: 'creator-2',
};

const draftStorage = new Map<string, string>();

Object.defineProperty(window, 'localStorage', {
  configurable: true,
  value: {
    clear: () => draftStorage.clear(),
    getItem: (key: string) => draftStorage.get(key) ?? null,
    removeItem: (key: string) => draftStorage.delete(key),
    setItem: (key: string, value: string) => draftStorage.set(key, value),
  },
});

describe('values editor store', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    window.localStorage.clear();
    vi.useFakeTimers();
    vi.mocked(listValuesRevisions).mockResolvedValue([draft, parent]);
    vi.mocked(listSecrets).mockResolvedValue([{ name: 'database', keys: ['password'] }]);
    vi.mocked(createValuesRevision).mockReset();
  });

  it('restores a safe local draft and recomputes its diff', async () => {
    window.localStorage.setItem('values_draft:definition-1', 'replicas: 3');
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');

    await store.load();

    expect(store.restoredDraft).toBe(true);
    expect(store.editorContent).toBe('replicas: 3');
    expect(store.diffResult.hasChanges).toBe(true);
  });

  it('uses the safe empty template when no revision exists', async () => {
    vi.mocked(listValuesRevisions).mockResolvedValue([]);
    vi.mocked(listSecrets).mockResolvedValue([]);
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');

    await store.load();

    expect(store.currentRevision).toBeNull();
    expect(store.parentRevision).toBeNull();
    expect(store.editorContent).toBe('# Paste or edit your values.yaml here\n{}');
  });

  it('keeps editor content when a reload fails with a network error', async () => {
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');
    store.setEditorContent('replicas: 4');
    await vi.advanceTimersByTimeAsync(500);
    vi.mocked(listValuesRevisions).mockRejectedValue(new ConnectError('transport unavailable', Code.Unavailable));

    await store.load();

    expect(store.editorContent).toBe('replicas: 4');
    expect(store.error).toContain('暂不可用');
  });

  it('never persists a suspected secret and removes an earlier safe draft', async () => {
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');
    window.localStorage.setItem(store.draftKey, 'replicas: 2');

    store.setEditorContent('password: my-secret-value');
    await vi.advanceTimersByTimeAsync(2000);

    expect(store.validationIssue?.code).toBe('secret_literal_forbidden');
    expect(window.localStorage.getItem(store.draftKey)).toBeNull();
  });

  it('locks duplicate saves and reports parent conflict without losing content', async () => {
    vi.mocked(listValuesRevisions).mockResolvedValue([parent]);
    const deferredSave = Promise.withResolvers<ValuesRevision>();
    vi.mocked(createValuesRevision).mockReturnValue(deferredSave.promise);
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');
    await store.load();
    store.setEditorContent('replicas: 4');
    await vi.advanceTimersByTimeAsync(500);

    const first = store.save();
    const second = await store.save();
    deferredSave.reject(new ConnectError('revision parent has changed', Code.FailedPrecondition, { 'X-Reason-Code': 'parent_conflict' }));
    await first;
    expect(second).toBe(false);
    expect(createValuesRevision).toHaveBeenCalledTimes(1);
    expect(store.editorContent).toBe('replicas: 4');
    expect(store.showConflictDialog).toBe(true);
  });

  it('blocks incomplete SecretRef before sending a request', async () => {
    vi.mocked(listValuesRevisions).mockResolvedValue([parent]);
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');
    await store.load();
    store.addSecretRef();

    expect(store.secretRefsError).toBe('请完成 SecretRef 配置');
    expect(await store.save()).toBe(false);
    expect(createValuesRevision).not.toHaveBeenCalled();
  });

  it('loads the prepared convergence snapshot and never reads browser drafts (AC-058-35/48)', async () => {
    window.localStorage.setItem('values_draft:definition-1', 'local: stale-draft');
    vi.mocked(getPrepareSession).mockResolvedValue({
      releaseDefinitionId: 'definition-1',
      parentRevisionId: 'parent-1',
      document: 'replicas: 5',
      lockedPaths: ['replicas'],
      expiresAt: '2026-08-22T10:00:00.000Z',
      taskIds: ['t1', 't2'],
      lockedPathsHash: 'hash-1',
      parentVersion: 3n,
    });
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');

    await store.loadConvergence('token-1');

    expect(store.convergenceMode).toBe(true);
    expect(store.editorContent).toBe('replicas: 5');
    expect(store.lockedPaths).toEqual(['replicas']);
    expect(store.preparedTaskIds).toEqual(['t1', 't2']);
    expect(store.restoredDraft).toBe(false);
    expect(window.localStorage.getItem(store.draftKey)).toBe('local: stale-draft');
  });

  it('creates a convergence draft with the token and consumes it once (AC-058-36/38)', async () => {
    vi.mocked(getPrepareSession).mockResolvedValue({
      releaseDefinitionId: 'definition-1',
      parentRevisionId: 'parent-1',
      document: 'replicas: 5',
      lockedPaths: ['replicas'],
      expiresAt: null,
      taskIds: ['t1'],
      lockedPathsHash: 'hash-1',
      parentVersion: 3n,
    });
    vi.mocked(createValuesRevision).mockResolvedValue({ ...draft, document: 'replicas: 5' });
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');
    await store.loadConvergence('token-1');
    vi.mocked(createValuesRevision).mockClear();

    expect(await store.save()).toBe(true);
    expect(createValuesRevision).toHaveBeenCalledWith(
      expect.objectContaining({ prepareToken: 'token-1', expectedParentVersion: 3, parentRevisionId: 'parent-1' }),
    );
    expect(store.prepareToken).toBe('');
    expect(store.toast).toContain('收敛 Draft');
  });

  it('wires the approval chain: submit/approve/reject/discard (AC-058-39~41)', async () => {
    vi.mocked(listValuesRevisions).mockResolvedValue([draft, parent]);
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');
    await store.load();

    const pending = { ...draft, status: 'pending_approval' as const };
    const approved = { ...draft, status: 'approved' as const };
    const rejected = { ...draft, status: 'rejected' as const };
    const discarded = { ...draft, status: 'discarded' as const };

    vi.mocked(submitValuesRevision).mockResolvedValue(pending);
    expect(await store.submit()).toBe(true);
    expect(submitValuesRevision).toHaveBeenCalledWith('draft-1', '1');
    expect(store.currentRevision?.status).toBe('pending_approval');

    vi.mocked(approveValuesRevision).mockResolvedValue(approved);
    expect(await store.approve()).toBe(true);
    expect(store.currentRevision?.status).toBe('approved');

    vi.mocked(rejectValuesRevision).mockResolvedValue(rejected);
    expect(await store.reject()).toBe(true);
    expect(store.currentRevision?.status).toBe('rejected');

    vi.mocked(discardValuesRevision).mockResolvedValue(discarded);
    expect(await store.discard()).toBe(true);
    expect(store.currentRevision?.status).toBe('discarded');
  });

});
