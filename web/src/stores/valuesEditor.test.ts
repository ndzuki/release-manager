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

  // AC-058-41: approval is a different actor's job, so their page load is the
  // only entry point to the revision waiting for approval.
  it('loads the pending-approval revision when there is no draft', async () => {
    const pending: ValuesRevision = { ...draft, id: 'pending-1', status: 'pending_approval' };
    vi.mocked(listValuesRevisions).mockResolvedValue([pending, parent]);
    vi.mocked(listSecrets).mockResolvedValue([]);
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');

    await store.load();

    expect(store.currentRevision?.id).toBe('pending-1');
    expect(store.currentRevision?.status).toBe('pending_approval');
    expect(store.parentRevision?.id).toBe('parent-1');
  });

  it('still prefers an editable draft over a pending-approval revision', async () => {
    const pending: ValuesRevision = { ...draft, id: 'pending-1', status: 'pending_approval' };
    vi.mocked(listValuesRevisions).mockResolvedValue([pending, draft, parent]);
    vi.mocked(listSecrets).mockResolvedValue([]);
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');

    await store.load();

    expect(store.currentRevision?.id).toBe('draft-1');
  });

  // TASK-270 review blocker ④ (real-service probe): createValuesRevision's
  // expected_parent_version is compared by the server with MAX(version) over the
  // definition's revisions (internal/store/*/values_lifecycle.go
  // validateValuesParent) — it is the CHAIN HEAD's version, not a revision's
  // state_version. `parent` is approved with version 1 and state_version 3
  // (draft→pending→approved), so sending state_version produced parent_conflict
  // while version was ACCEPTED (new version=2). ValuesRevisionActions offers
  // "保存 Draft" whenever revision === null, so this is the path a second
  // revision takes when the chain head is approved and nothing is in flight.
  it('anchors a new draft on the chain head version, not the parent state_version', async () => {
    vi.mocked(listValuesRevisions).mockResolvedValue([parent]);
    vi.mocked(listSecrets).mockResolvedValue([]);
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');

    await store.load();

    expect(store.currentRevision).toBeNull();
    expect(store.parentRevision?.id).toBe('parent-1');
    expect(store.parentRevision?.revision).toBe(1);
    expect(store.parentRevision?.stateVersion).toBe('3');
    expect(store.chainHeadVersion).toBe(1);

    vi.mocked(createValuesRevision).mockResolvedValue({ ...draft, document: '{"replicas":4}' });
    store.setEditorContent('{"replicas":4}');
    await vi.advanceTimersByTimeAsync(500);

    expect(await store.save()).toBe(true);
    // Explicit assertion on the value handed to the connect layer: the chain head
    // VERSION (1), never the stateVersion (3) that the server refuses.
    expect(createValuesRevision).toHaveBeenCalledWith(
      expect.objectContaining({ parentRevisionId: 'parent-1', expectedParentVersion: 1 }),
    );
  });

  // The anchor is the CHAIN HEAD, which may not be the approved content parent: a
  // discarded head still occupies MAX(version). Anchoring on
  // parentRevision.revision (1) instead of the head (2) would be refused here.
  it('anchors on the head version when the head is not the approved parent', async () => {
    const discardedHead: ValuesRevision = {
      ...draft, id: 'discarded-2', revision: 2, stateVersion: '2', status: 'discarded',
      parentRevisionId: parent.id,
    };
    vi.mocked(listValuesRevisions).mockResolvedValue([discardedHead, parent]);
    vi.mocked(listSecrets).mockResolvedValue([]);
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');

    await store.load();

    expect(store.currentRevision).toBeNull();
    expect(store.parentRevision?.id).toBe('parent-1');
    expect(store.chainHeadVersion).toBe(2);

    vi.mocked(createValuesRevision).mockResolvedValue({ ...draft, document: '{"replicas":4}' });
    store.setEditorContent('{"replicas":4}');
    await vi.advanceTimersByTimeAsync(500);

    expect(await store.save()).toBe(true);
    expect(createValuesRevision).toHaveBeenCalledWith(
      expect.objectContaining({ parentRevisionId: 'parent-1', expectedParentVersion: 2 }),
    );
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

// AC-055-13: locked paths are read-only in convergence mode. UX-003: submit sends
// the PERSISTED revision, so unsaved editor content must be refused instead of
// quietly approving the previous saved draft.
describe('locked paths and unsaved content (AC-055-13 / UX-003)', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
  });

  // Convergence mode never reads the browser draft: the editor starts from the
  // PREPARED payload, so `save()` (which creates the bound draft) must run before
  // a submit can be accepted. Documents are JSON-in-YAML here for brevity.
  function preparedSession(document = '{"replicas":5}') {
    return {
      releaseDefinitionId: 'definition-1',
      parentRevisionId: 'parent-1',
      document,
      lockedPaths: ['replicas'],
      expiresAt: null,
      taskIds: ['t1'],
      lockedPathsHash: 'hash-1',
      parentVersion: 3n,
    };
  }

  async function storeWith(preparedDocument = '{"replicas":5}') {
    vi.mocked(getPrepareSession).mockResolvedValue(preparedSession(preparedDocument) as never);
    vi.mocked(listValuesRevisions).mockResolvedValue([parent, draft]);
    vi.mocked(listSecrets).mockResolvedValue([]);
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');
    await store.loadConvergence('token-1');
    return store;
  }

  /** Runs the real convergence flow up to a saved draft bound to the prepared doc. */
  async function savedStore(preparedDocument = '{"replicas":5}') {
    const store = await storeWith(preparedDocument);
    vi.mocked(createValuesRevision).mockResolvedValue({
      ...draft,
      document: preparedDocument,
      convergenceTaskIds: ['t1'],
    });
    expect(await store.save()).toBe(true);
    return store;
  }

  it('refuses a saved draft whose locked path differs from the approved parent', async () => {
    // save() does not check locks, so a prepared payload that changes a locked path
    // reaches submit as a saved draft. The baseline must be the APPROVED parent —
    // comparing canonicalCurrent (which tracks the editor) against itself made this
    // guard unreachable in production.
    const store = await savedStore('{"replicas":99}');
    vi.mocked(submitValuesRevision).mockClear();

    expect(store.hasUnsavedChanges).toBe(false);
    expect(await store.submit()).toBe(false);
    expect(store.error).toContain('replicas');
    expect(store.error).toContain('锁定路径');
    expect(submitValuesRevision).not.toHaveBeenCalled();
  });

  it('submits when the saved draft keeps the locked paths at the parent values', async () => {
    const store = await savedStore('{"replicas":1}');
    vi.mocked(submitValuesRevision).mockClear();
    vi.mocked(submitValuesRevision).mockResolvedValue({ ...draft, status: 'pending_approval' });

    expect(await store.submit()).toBe(true);
    expect(submitValuesRevision).toHaveBeenCalledTimes(1);
  });

  it('refuses a submit before the prepared payload has been saved (UX-003)', async () => {
    const store = await storeWith();
    vi.mocked(submitValuesRevision).mockClear();

    // The editor holds the prepared payload but no draft carries it yet, so
    // submitting would approve the PARENT draft instead of what is on screen.
    expect(store.hasUnsavedChanges).toBe(true);
    expect(await store.submit()).toBe(false);
    expect(store.error).toContain('保存');
    expect(submitValuesRevision).not.toHaveBeenCalled();
  });

  it('refuses unsaved edits made after the save (UX-003)', async () => {
    const store = await savedStore();
    vi.mocked(submitValuesRevision).mockClear();
    store.editorContent = '{"replicas": 42}';

    expect(store.hasUnsavedChanges).toBe(true);
    expect(await store.submit()).toBe(false);
    expect(store.error).toContain('保存');
    expect(submitValuesRevision).not.toHaveBeenCalled();
  });

  it('refuses to submit invalid content (UX-003)', async () => {
    const store = await savedStore();
    vi.mocked(submitValuesRevision).mockClear();
    store.editorContent = 'replicas: [unclosed';

    expect(await store.submit()).toBe(false);
    expect(submitValuesRevision).not.toHaveBeenCalled();
  });

  it('treats a valid but edited SecretRef as unsaved (UX-003)', async () => {
    const store = await savedStore();
    vi.mocked(submitValuesRevision).mockClear();
    store.addSecretRef();
    store.updateSecretRef(store.secretRefs[0]!.id, { name: 'database', key: 'password', namespace: 'default' });

    expect(store.hasUnsavedChanges).toBe(true);
    expect(await store.submit()).toBe(false);
    expect(submitValuesRevision).not.toHaveBeenCalled();
  });

  it('takes the locked-path baseline from the prepared session parent, not the first approved', async () => {
    // Two approved revisions with DIFFERENT values at the locked path. If the
    // baseline were "the first approved in the list", this saved draft would look
    // like it changed the locked path and the submit would be refused.
    const olderApproved: ValuesRevision = { ...parent, id: 'parent-0', revision: 1, document: '{"replicas":1}' };
    const preparedParent: ValuesRevision = { ...parent, id: 'parent-9', revision: 3, document: '{"replicas":7}' };
    vi.mocked(getPrepareSession).mockResolvedValue({
      ...preparedSession('{"replicas":7}'),
      parentRevisionId: 'parent-9',
    } as never);
    vi.mocked(listValuesRevisions).mockResolvedValue([olderApproved, preparedParent, draft]);
    vi.mocked(listSecrets).mockResolvedValue([]);
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');
    await store.loadConvergence('token-1');
    vi.mocked(createValuesRevision).mockResolvedValue({ ...draft, document: '{"replicas":7}' });
    expect(await store.save()).toBe(true);
    vi.mocked(submitValuesRevision).mockClear();
    vi.mocked(submitValuesRevision).mockResolvedValue({ ...draft, status: 'pending_approval' });

    expect(await store.submit()).toBe(true);
    expect(submitValuesRevision).toHaveBeenCalledTimes(1);
  });

  it('refuses to submit when the approved parent baseline is missing (fail-closed)', async () => {
    vi.mocked(getPrepareSession).mockResolvedValue(preparedSession('{"replicas":5}') as never);
    // No approved revision is returned, so there is nothing to compare locked paths
    // against; the guard must refuse rather than fall back to the editor itself.
    vi.mocked(listValuesRevisions).mockResolvedValue([draft]);
    vi.mocked(listSecrets).mockResolvedValue([]);
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');
    await store.loadConvergence('token-1');
    vi.mocked(createValuesRevision).mockResolvedValue({ ...draft, document: '{"replicas":5}' });
    await store.save();
    vi.mocked(submitValuesRevision).mockClear();

    expect(await store.submit()).toBe(false);
    expect(store.error).toContain('基线');
    expect(submitValuesRevision).not.toHaveBeenCalled();
  });

  it('does not call secretRefs unsaved when only server-persisted fields match', async () => {
    const withRefs: ValuesRevision = {
      ...draft,
      secretRefs: [{ path: '.secrets.db.password', name: 'database', key: 'password' }],
    };
    vi.mocked(getPrepareSession).mockResolvedValue(preparedSession('{"replicas":5}') as never);
    vi.mocked(listValuesRevisions).mockResolvedValue([parent, withRefs]);
    // The ref must be valid, otherwise saveDisabled (secretRefsError) blocks save.
    vi.mocked(listSecrets).mockResolvedValue([{ name: 'database', keys: ['password'] }]);
    const store = useValuesEditorStore();
    store.resetScope('definition-1', 'cluster-1');
    await store.loadConvergence('token-1');
    vi.mocked(createValuesRevision).mockResolvedValue({ ...withRefs, document: '{"replicas":5}' });
    expect(await store.save()).toBe(true);

    // namespace/path are dropped by mapSecretRef, so they must not count as edits.
    expect(store.secretRefs).toHaveLength(1);
    expect(store.hasUnsavedChanges).toBe(false);
  });

  it('reports no unsaved changes once the saved draft carries the prepared payload', async () => {
    const store = await savedStore();
    expect(store.hasUnsavedChanges).toBe(false);
  });
});
