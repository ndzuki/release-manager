import { defineStore } from 'pinia';
import { computed, ref } from 'vue';
import {
  approveValuesRevision,
  createValuesRevision,
  listSecrets,
  listValuesRevisions,
  rejectValuesRevision,
  valuesError,
} from '@/connect/values-revision';
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
  const approving = ref(false);
  const error = ref<string | null>(null);
  const conflictDetected = ref(false);
  const showConflictDialog = ref(false);
  const draftLoaded = ref(false);
  const restoredDraft = ref(false);
  const toast = ref<string | null>(null);
  let editorTimer: ReturnType<typeof setTimeout> | undefined;
  let draftTimer: ReturnType<typeof setTimeout> | undefined;

  const draftKeyValue = computed(() => draftKey(releaseDefinitionId.value));
  const secretRefsError = computed(() => validateSecretRefs(secretRefs.value, availableSecrets.value));
  const validationError = computed(() => validationIssue.value?.message ?? null);
  const canEdit = computed(() => currentRevision.value?.status !== 'approved' && currentRevision.value?.status !== 'superseded');
  const canApprove = computed(() => currentRevision.value?.status === 'draft');
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
  }

  async function load(): Promise<void> {
    if (!releaseDefinitionId.value || !clusterId.value) return;
    loading.value = true;
    error.value = null;
    try {
      const revisions = await listValuesRevisions(releaseDefinitionId.value);
      parentRevision.value = revisions.find((revision) => revision.status === 'approved') ?? null;
      currentRevision.value = revisions.find((revision) => revision.status === 'draft') ?? null;
      const savedDraft = storage.getItem(draftKeyValue.value);
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
      const result = await createValuesRevision({
        releaseDefinitionId: releaseDefinitionId.value,
        parentRevisionId: parentRevision.value?.id ?? '',
        document: editorContent.value,
        secretRefs: secretRefs.value.map((item) => ({ path: item.path, name: item.name, key: item.key, namespace: item.namespace })),
        expectedParentVersion: parentRevision.value?.version ?? 0,
      });
      currentRevision.value = result;
      storage.removeItem(draftKeyValue.value);
      draftLoaded.value = false;
      restoredDraft.value = false;
      toast.value = '已保存为 Draft';
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

  async function approve(): Promise<boolean> {
    if (!currentRevision.value || approving.value || !canApprove.value) return false;
    approving.value = true;
    error.value = null;
    try {
      currentRevision.value = await approveValuesRevision(currentRevision.value.id, currentRevision.value.version);
      toast.value = '已审批通过';
      return true;
    } catch (requestError) {
      error.value = valuesError(requestError).message;
      return false;
    } finally {
      approving.value = false;
    }
  }

  async function reject(reason: string): Promise<boolean> {
    if (!currentRevision.value || approving.value || !canApprove.value || reason.length > 1000) return false;
    approving.value = true;
    error.value = null;
    try {
      currentRevision.value = await rejectValuesRevision(currentRevision.value.id, currentRevision.value.version, reason);
      toast.value = '已拒绝该 Revision';
      return true;
    } catch (requestError) {
      error.value = valuesError(requestError).message;
      return false;
    } finally {
      approving.value = false;
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
    approving,
    error,
    conflictDetected,
    showConflictDialog,
    draftLoaded,
    restoredDraft,
    toast,
    draftKey: draftKeyValue,
    secretRefsError,
    validationError,
    canEdit,
    canApprove,
    editable,
    saveDisabled,
    resetScope,
    load,
    reloadParent,
    setEditorContent,
    setEditorLanguage,
    setEditable,
    addSecretRef,
    removeSecretRef,
    updateSecretRef,
    save,
    approve,
    reject,
    dispose,
  };
});