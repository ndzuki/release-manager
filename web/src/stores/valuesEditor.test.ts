import { createPinia, setActivePinia } from 'pinia';
import { ConnectError, Code } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useValuesEditorStore } from './valuesEditor';
import type { ValuesRevision } from '@/types/valuesRevision';
import {
  approveValuesRevision,
  createValuesRevision,
  listSecrets,
  listValuesRevisions,
  rejectValuesRevision,
} from '@/connect/values-revision';

vi.mock('@/connect/values-revision', async (importOriginal) => {
  const original = await importOriginal<typeof import('@/connect/values-revision')>();
  return {
    ...original,
    approveValuesRevision: vi.fn(),
    createValuesRevision: vi.fn(),
    listSecrets: vi.fn(),
    listValuesRevisions: vi.fn(),
    rejectValuesRevision: vi.fn(),
  };
});

const parent: ValuesRevision = {
  id: 'parent-1', releaseDefinitionId: 'definition-1', revision: 1, version: 3,
  document: '{"replicas":1}', valuesDigest: 'sha256:parent', status: 'approved', parentRevisionId: null,
  secretRefs: [], createdBy: 'creator-1', createdAt: '2026-07-23T00:00:00Z',
};

const draft: ValuesRevision = {
  ...parent,
  id: 'draft-1', revision: 2, version: 1, document: '{"replicas":2}', valuesDigest: 'sha256:draft',
  status: 'draft', parentRevisionId: parent.id, createdBy: 'creator-2',
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
    vi.mocked(approveValuesRevision).mockReset();
    vi.mocked(rejectValuesRevision).mockReset();
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

  it('locks duplicate approval requests', async () => {
    const deferredApproval = Promise.withResolvers<ValuesRevision>();
    vi.mocked(approveValuesRevision).mockReturnValue(deferredApproval.promise);
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');
    await store.load();

    const first = store.approve();
    const second = await store.approve();
    deferredApproval.resolve({ ...draft, status: 'approved', version: 2, approvedBy: 'admin-1', approvedAt: '2026-07-23T01:00:00Z' });
    await first;

    expect(second).toBe(false);
    expect(approveValuesRevision).toHaveBeenCalledTimes(1);
    expect(store.currentRevision?.status).toBe('approved');
  });
});
