import { defineStore } from 'pinia';
import { computed, ref } from 'vue';
import {
  approveValuesRevision,
  createValuesRevision,
  discardValuesRevision,
  listSecrets,
  listValuesRevisions,
  rejectValuesRevision,
  submitValuesRevision,
  valuesError,
} from '@/connect/values-revision';
import { getPrepareSession } from '@/connect/emergency-api';
import type {
  DiffResult,
  EditorLanguage,
  SecretOption,
  SecretRef,
  SecretRefFormItem,
  ValidationIssue,
  ValuesRevision,
} from '@/types/valuesRevision';
import { canonicalDiff } from '@/utils/valuesCanonical';
import { validateSecretRefs, validateValuesDocument } from '@/utils/valuesValidation';

const EMPTY_TEMPLATE = '# Paste or edit your values.yaml here\n{}';
const DRAFT_PREFIX = 'values_draft:';

interface DraftStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

const storage: DraftStorage = {
  getItem: (key) => window.localStorage.getItem(key),
  setItem: (key, value) => window.localStorage.setItem(key, value),
  removeItem: (key) => window.localStorage.removeItem(key),
};

function makeSecretRef(): SecretRefFormItem {
  return { id: crypto.randomUUID(), path: '', name: '', key: '' };
}

function draftKey(releaseDefinitionId: string): string {
  return `${DRAFT_PREFIX}${releaseDefinitionId}`;
}

export const useValuesEditorStore = defineStore('valuesEditor', () => {
  const releaseDefinitionId = ref('');
  const clusterId = ref('');
  const currentRevision = ref<ValuesRevision | null>(null);
  const parentRevision = ref<ValuesRevision | null>(null);
  const editorContent = ref(EMPTY_TEMPLATE);
  const editorLanguage = ref<EditorLanguage>('yaml');
  const canonicalCurrent = ref<unknown | null>(null);
  const validationIssue = ref<ValidationIssue | null>(null);
  const diffResult = ref<DiffResult>({ changes: [], hasChanges: false });
  const secretRefs = ref<SecretRefFormItem[]>([]);
  const availableSecrets = ref<SecretOption[]>([]);
  const loading = ref(false);
  const saving = ref(false);
  const error = ref<string | null>(null);
  const conflictDetected = ref(false);
  const showConflictDialog = ref(false);
  const draftLoaded = ref(false);
  const restoredDraft = ref(false);
  const toast = ref<string | null>(null);
  // Convergence mode (REQ-058 Step 8): prepareToken + locked paths from the
  // Prepare Session. Locked values are rendered read-only and re-verified by
  // the server at Approve; prepared payloads are never written to browser
  // storage (AC-058-35/48).
  const convergenceMode = ref(false);
  const prepareToken = ref('');
  const lockedPaths = ref<string[]>([]);
  const preparedTaskIds = ref<string[]>([]);
  const approving = ref(false);
  const discarding = ref(false);
  const convergenceParentRevisionId = ref('');
  const convergenceParentVersion = ref(0);
  let editorTimer: ReturnType<typeof setTimeout> | undefined;
  let draftTimer: ReturnType<typeof setTimeout> | undefined;

  const draftKeyValue = computed(() => draftKey(releaseDefinitionId.value));
  const secretRefsError = computed(() => validateSecretRefs(secretRefs.value, availableSecrets.value));
  const validationError = computed(() => validationIssue.value?.message ?? null);
  const canEdit = computed(
    () =>
      currentRevision.value?.status !== 'approved' &&
      currentRevision.value?.status !== 'superseded' &&
      currentRevision.value?.status !== 'discarded',
  );
  const editable = ref(true);
  const saveDisabled = computed(() => saving.value || !editable.value || !canEdit.value || Boolean(validationError.value) || Boolean(secretRefsError.value));

  /** Canonical JSON of the persisted document, or null when it does not parse. */
  const persistedCanonical = computed(() => {
    const document = currentRevision.value?.document;
    if (!document) return null;
    const parsed = validateValuesDocument(document);
    return parsed.issue ? null : JSON.stringify(parsed.canonical.value);
  });

  /**
   * Canonical form of the approved parent document — the baseline locked paths are
   * read-only against.
   */
  const parentCanonical = computed<unknown | null>(() => {
    const document = parentRevision.value?.document;
    if (!document) return null;
    const parsed = validateValuesDocument(document);
    return parsed.issue ? null : parsed.canonical.value;
  });

  /**
   * UX-003 (blocker): `submit` sends the PERSISTED revision id/stateVersion, so
   * submitting while the editor holds unsaved or invalid content silently approves
   * the previous saved draft instead of what the user is looking at. Anything that
   * is not the persisted document counts as unsaved.
   */
  /**
   * The persisted SecretRefs, canonicalised for comparison (order-insensitive).
   * Only name+key: `mapSecretRef` (connect/values-revision.ts) drops `path` and
   * `namespace`, so comparing those fields would report "unsaved" forever once a
   * ref exists.
   */
  function secretRefsKey(items: { name: string; key: string }[]): string {
    return JSON.stringify(
      items
        .map((item) => ({ name: item.name, key: item.key }))
        .sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b))),
    );
  }

  const persistedSecretRefs = computed(() => secretRefsKey(currentRevision.value?.secretRefs ?? []));
  const editedSecretRefs = computed(() => secretRefsKey(secretRefs.value));

  const hasUnsavedChanges = computed(() => {
    const revision = currentRevision.value;
    if (!revision) return false;
    if (secretRefsError.value) return true;
    // A valid-but-EDITED SecretRef is also unsaved: submitValuesRevision carries no
    // secretRefs, so submitting would drop the change just like a document edit.
    if (persistedSecretRefs.value !== editedSecretRefs.value) return true;
    // Canonicalise the LIVE editor content instead of reading canonicalCurrent:
    // that one is recomputed on a 500ms debounce, so during the typing window it
    // still describes the previous content and the guard would report "saved".
    const parsed = validateValuesDocument(editorContent.value);
    if (parsed.issue) return true;
    const persisted = persistedCanonical.value;
    if (persisted === null) return editorContent.value.trim() !== revision.document.trim();
    return JSON.stringify(parsed.canonical.value) !== persisted;
  });

  function clearTimers(): void {
    if (editorTimer) clearTimeout(editorTimer);
    if (draftTimer) clearTimeout(draftTimer);
    editorTimer = undefined;
    draftTimer = undefined;
  }

  function canonicalizeAndDiff(): void {
    const result = validateValuesDocument(editorContent.value);
    canonicalCurrent.value = result.canonical.value;
    validationIssue.value = result.issue;
    if (result.issue?.code === 'secret_literal_forbidden') storage.removeItem(draftKeyValue.value);
    if (result.issue || result.canonical.value === null) {
      diffResult.value = { changes: [], hasChanges: false };
      return;
    }
    try {
      const parentValue = parentRevision.value ? JSON.parse(parentRevision.value.document) : {};
      diffResult.value = canonicalDiff(result.canonical.value, parentValue);
    } catch {
      diffResult.value = { changes: [], hasChanges: false };
      error.value = '服务端返回的 canonical document 无法解析';
    }
  }

  function scheduleRecompute(): void {
    if (editorTimer) clearTimeout(editorTimer);
    editorTimer = setTimeout(() => {
      canonicalizeAndDiff();
      editorTimer = undefined;
    }, 500);
    if (draftTimer) clearTimeout(draftTimer);
    draftTimer = setTimeout(() => {
      // Convergence mode never persists prepared documents/locked values to
      // browser storage (AC-058-35/48).
      if (convergenceMode.value) {
        draftTimer = undefined;
        return;
      }
      const result = validateValuesDocument(editorContent.value);
      if (!result.issue) storage.setItem(draftKeyValue.value, editorContent.value);
      else if (result.issue.code === 'secret_literal_forbidden') storage.removeItem(draftKeyValue.value);
      draftTimer = undefined;
    }, 2000);
  }

  function setEditorContent(content: string): void {
    editorContent.value = content;
    scheduleRecompute();
  }

  function setEditorLanguage(language: EditorLanguage): void {
    editorLanguage.value = language;
  }

  function setEditable(value: boolean): void {
    editable.value = value;
  }

  function resetScope(nextReleaseDefinitionId: string, nextClusterId: string): void {
    clearTimers();
    releaseDefinitionId.value = nextReleaseDefinitionId;
    clusterId.value = nextClusterId;
    currentRevision.value = null;
    parentRevision.value = null;
    editorContent.value = EMPTY_TEMPLATE;
    validationIssue.value = null;
    diffResult.value = { changes: [], hasChanges: false };
    secretRefs.value = [];
    availableSecrets.value = [];
    error.value = null;
    conflictDetected.value = false;
    showConflictDialog.value = false;
    draftLoaded.value = false;
    restoredDraft.value = false;
    editable.value = true;
    toast.value = null;
    convergenceMode.value = false;
    prepareToken.value = '';
    lockedPaths.value = [];
    preparedTaskIds.value = [];
    approving.value = false;
    discarding.value = false;
    convergenceParentRevisionId.value = '';
    convergenceParentVersion.value = 0;
  }

  async function load(): Promise<void> {
    if (!releaseDefinitionId.value || !clusterId.value) return;
    loading.value = true;
    error.value = null;
    try {
      const revisions = await listValuesRevisions(releaseDefinitionId.value);
      // In convergence mode the authoritative parent is the prepared session's
      // parent, not "the first approved in the list" (several may exist).
      const preparedParent = convergenceParentRevisionId.value
        ? revisions.find((revision) => revision.id === convergenceParentRevisionId.value)
        : undefined;
      parentRevision.value = preparedParent ?? revisions.find((revision) => revision.status === 'approved') ?? null;
      currentRevision.value = revisions.find((revision) => revision.status === 'draft') ?? null;
      // Convergence mode never reads browser drafts — prepared payloads are
      // rebuilt from the canonical API only (AC-058-35/48).
      const savedDraft = convergenceMode.value ? null : storage.getItem(draftKeyValue.value);
      draftLoaded.value = savedDraft !== null;
      restoredDraft.value = savedDraft !== null;
      editorContent.value = savedDraft ?? currentRevision.value?.document ?? parentRevision.value?.document ?? EMPTY_TEMPLATE;
      secretRefs.value = currentRevision.value?.secretRefs.map((item) => ({ ...item, id: crypto.randomUUID() })) ?? [];
      try {
        availableSecrets.value = await listSecrets(clusterId.value, releaseDefinitionId.value);
      } catch (secretError) {
        availableSecrets.value = [];
        const mapped = valuesError(secretError);
        if (mapped.code !== 'release_definition_not_found') error.value = mapped.message;
      }
      canonicalizeAndDiff();
    } catch (requestError) {
      error.value = valuesError(requestError).message;
    } finally {
      loading.value = false;
    }
  }

  async function reloadParent(): Promise<void> {
    if (!releaseDefinitionId.value) return;
    const revisions = await listValuesRevisions(releaseDefinitionId.value);
    parentRevision.value = revisions.find((revision) => revision.status === 'approved') ?? null;
    canonicalizeAndDiff();
    conflictDetected.value = false;
    showConflictDialog.value = false;
  }

  /**
   * Convergence mode (REQ-058 Step 8): loads the prepared session snapshot —
   * editable document + locked paths + task ids — and anchors the CAS parent
   * version for the single-consumption draft create.
   */
  async function loadConvergence(token: string): Promise<void> {
    convergenceMode.value = true;
    prepareToken.value = token;
    try {
      const prepared = await getPrepareSession(token);
      lockedPaths.value = [...prepared.lockedPaths];
      preparedTaskIds.value = [...prepared.taskIds];
      // Set the parent id BEFORE load(): load() picks the locked-path baseline from
      // it, and picking "the first approved revision in the list" instead would use
      // the wrong parent whenever more than one approved revision exists.
      convergenceParentRevisionId.value = prepared.parentRevisionId;
      convergenceParentVersion.value = Number(prepared.parentVersion);
      await load();
      editorContent.value = prepared.document;
      canonicalizeAndDiff();
    } catch (requestError) {
      error.value = valuesError(requestError).message;
    }
  }

  function addSecretRef(): void {
    secretRefs.value.push(makeSecretRef());
  }

  function removeSecretRef(id: string): void {
    secretRefs.value = secretRefs.value.filter((item) => item.id !== id);
  }

  function updateSecretRef(id: string, patch: Partial<SecretRef>): void {
    const item = secretRefs.value.find((candidate) => candidate.id === id);
    if (!item) return;
    Object.assign(item, patch);
  }

  async function save(): Promise<boolean> {
    if (saveDisabled.value || saving.value) return false;
    saving.value = true;
    error.value = null;
    try {
      const stateVer = parentRevision.value?.stateVersion;
      const result = await createValuesRevision({
        releaseDefinitionId: releaseDefinitionId.value,
        parentRevisionId: convergenceMode.value
          ? convergenceParentRevisionId.value
          : (parentRevision.value?.id ?? ''),
        document: editorContent.value,
        secretRefs: secretRefs.value.map((item) => ({ name: item.name, key: item.key, namespace: item.namespace })),
        expectedParentVersion: convergenceMode.value
          ? convergenceParentVersion.value
          : (stateVer ? Number(stateVer) : 0),
        prepareToken: convergenceMode.value ? prepareToken.value : undefined,
      });
      currentRevision.value = result;
      if (convergenceMode.value) {
        // Single consumption (AC-058-36): the token is spent by the draft
        // create; later saves in this scope must not replay it.
        prepareToken.value = '';
      }
      storage.removeItem(draftKeyValue.value);
      draftLoaded.value = false;
      restoredDraft.value = false;
      toast.value = convergenceMode.value ? '已创建收敛 Draft（任务已绑定）' : '已保存为 Draft';
      return true;
    } catch (requestError) {
      const mapped = valuesError(requestError);
      error.value = mapped.message;
      if (mapped.code === 'parent_conflict') {
        conflictDetected.value = true;
        showConflictDialog.value = true;
      }
      return false;
    } finally {
      saving.value = false;
    }
  }

  /** Submit the current draft → pending_approval (explicit only, AC-058-38). */
/**
 * resolvePath walks a dotted YAML path (e.g. "api.image.digest") through a
 * parsed document. Returns undefined when any segment is missing, which is
 * itself a difference: a locked path that disappeared has changed.
 */
function resolvePath(document: unknown, path: string): unknown {
  let cursor: unknown = document;
  for (const segment of path.split('.')) {
    if (cursor === null || typeof cursor !== 'object') return undefined;
    cursor = (cursor as Record<string, unknown>)[segment];
  }
  return cursor;
}

/**
 * changedLockedPaths returns the locked paths whose value in the current draft
 * differs from the baseline the convergence session loaded (AC-055-13).
 *
 * The server re-verifies every locked path inside the approval transaction, so
 * this is not the security boundary -- it is the client-side guard that stops a
 * doomed submit and tells the user which path is locked.
 */
function changedLockedPaths(current: unknown, baseline: unknown, paths: string[]): string[] {
  const changed: string[] = [];
  for (const path of paths) {
    const before = resolvePath(baseline, path);
    const after = resolvePath(current, path);
    if (JSON.stringify(before) !== JSON.stringify(after)) changed.push(path);
  }
  return changed;
}

  async function submit(): Promise<boolean> {
    const revision = currentRevision.value;
    if (!revision || approving.value || saving.value) return false;
    // UX-003: never submit something other than what the user sees.
    if (saveDisabled.value) {
      error.value = '当前内容未通过校验，请修正后再提交';
      return false;
    }
    if (hasUnsavedChanges.value) {
      error.value = '请先保存 Draft；提交的是已保存的 Revision';
      return false;
    }
    // AC-055-13: locked paths are read-only in convergence mode. Refuse a draft
    // that changed one, and name it, instead of letting the server reject the
    // whole transaction without saying which path was at fault.
    if (convergenceMode.value && lockedPaths.value.length > 0) {
      const parsed = validateValuesDocument(editorContent.value);
      // Baseline is the APPROVED parent, not canonicalCurrent: the latter tracks
      // the editor (debounced recompute), so after the debounce the comparison was
      // the editor against itself and the guard silently passed. Falling back to
      // canonicalCurrent would restore exactly that hole, so a missing baseline is
      // fail-closed instead.
      if (parentCanonical.value === null) {
        error.value = '缺少已批准父 Revision 的基线，无法校验锁定路径；请刷新后重试';
        return false;
      }
      const changed = changedLockedPaths(parsed.canonical.value, parentCanonical.value, lockedPaths.value);
      if (changed.length > 0) {
        error.value = `锁定路径不可修改：${changed.join('、')}`;
        return false;
      }
    }
    approving.value = true;
    error.value = null;
    try {
      currentRevision.value = await submitValuesRevision(revision.id, revision.stateVersion);
      toast.value = '已提交审批';
      return true;
    } catch (requestError) {
      error.value = valuesError(requestError).message;
      return false;
    } finally {
      approving.value = false;
    }
  }

  /** Approve (different actor): server verifies all locked paths atomically. */
  async function approve(): Promise<boolean> {
    const revision = currentRevision.value;
    if (!revision || approving.value) return false;
    approving.value = true;
    error.value = null;
    try {
      currentRevision.value = await approveValuesRevision(revision.id, revision.stateVersion);
      toast.value = '已批准，收敛任务已标记 converged';
      return true;
    } catch (requestError) {
      error.value = valuesError(requestError).message;
      return false;
    } finally {
      approving.value = false;
    }
  }

  /** Reject: atomically unbinds the convergence tasks (AC-058-40). */
  async function reject(reason = ''): Promise<boolean> {
    const revision = currentRevision.value;
    if (!revision || approving.value) return false;
    approving.value = true;
    error.value = null;
    try {
      currentRevision.value = await rejectValuesRevision(revision.id, revision.stateVersion, reason);
      toast.value = '已拒绝，任务已解绑';
      return true;
    } catch (requestError) {
      error.value = valuesError(requestError).message;
      return false;
    } finally {
      approving.value = false;
    }
  }

  /** Explicit creator-only Discard (never automatic — AC-058-39). */
  async function discard(): Promise<boolean> {
    const revision = currentRevision.value;
    if (!revision || discarding.value) return false;
    discarding.value = true;
    error.value = null;
    try {
      currentRevision.value = await discardValuesRevision(revision.id, revision.stateVersion);
      toast.value = 'Draft 已丢弃，任务已解绑';
      return true;
    } catch (requestError) {
      error.value = valuesError(requestError).message;
      return false;
    } finally {
      discarding.value = false;
    }
  }

  function dispose(): void {
    clearTimers();
  }

  return {
    releaseDefinitionId,
    clusterId,
    currentRevision,
    parentRevision,
    editorContent,
    editorLanguage,
    canonicalCurrent,
    validationIssue,
    diffResult,
    secretRefs,
    availableSecrets,
    loading,
    saving,
    error,
    conflictDetected,
    showConflictDialog,
    draftLoaded,
    restoredDraft,
    toast,
    convergenceMode,
    prepareToken,
    lockedPaths,
    preparedTaskIds,
    approving,
    discarding,
    draftKey: draftKeyValue,
    secretRefsError,
    validationError,
    canEdit,
    editable,
    saveDisabled,
    hasUnsavedChanges,
    resetScope,
    load,
    loadConvergence,
    reloadParent,
    setEditorContent,
    setEditorLanguage,
    setEditable,
    addSecretRef,
    removeSecretRef,
    updateSecretRef,
    save,
    submit,
    approve,
    reject,
    discard,
    dispose,
  };
});
