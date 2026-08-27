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
      parentRevision.value = revisions.find((revision) => revision.status === 'approved') ?? null;
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
    await load();
    try {
      const prepared = await getPrepareSession(token);
      lockedPaths.value = [...prepared.lockedPaths];
      preparedTaskIds.value = [...prepared.taskIds];
      convergenceParentRevisionId.value = prepared.parentRevisionId;
      convergenceParentVersion.value = Number(prepared.parentVersion);
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
  async function submit(): Promise<boolean> {
    const revision = currentRevision.value;
    if (!revision || approving.value) return false;
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
  async function reject(): Promise<boolean> {
    const revision = currentRevision.value;
    if (!revision || approving.value) return false;
    approving.value = true;
    error.value = null;
    try {
      currentRevision.value = await rejectValuesRevision(revision.id, revision.stateVersion);
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
