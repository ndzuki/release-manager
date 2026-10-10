import { defineStore } from 'pinia';
import { computed, reactive, ref, watch } from 'vue';
import {
  createOperation,
  loadOperationOptions,
  mapOperationError,
  rollbackRelease,
  type OperationAPIError,
} from '@/connect/operation-api';
import { listApprovedValuesRevisions } from '@/connect/values-revision';
import type {
  BundleSummary,
  OperationType,
  PatchOverride,
} from '@/types/operation';
import type { ValuesRevision } from '@/types/valuesRevision';

interface DraftPayload {
  operationType: OperationType;
  bundleId: string | null;
  valuesRevisionId: string | null;
  patch: PatchOverride[];
  targetRevision: number | null;
}

interface OperationFormFields {
  operationType: OperationType;
  bundleId: string | null;
  valuesRevisionId: string | null;
  patch: PatchOverride[];
  expectedCurrentRevision: number | null;
  targetRevision: number | null;
}

export interface OperationFormErrors {
  targetRevision?: string;
  bundleId?: string;
  valuesRevisionId?: string;
  expectedCurrentRevision?: string;
  patch?: string;
  patchIndex?: number;
}

const patchPathPattern = /^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$/;

export const useOperationFormStore = defineStore('operationForm', () => {
  const releaseDefinitionId = ref<string | null>(null);
  const fields = reactive<OperationFormFields>({
    operationType: 'INSTALL',
    bundleId: null,
    valuesRevisionId: null,
    patch: [],
    expectedCurrentRevision: null,
    targetRevision: null,
  });
  const availableBundles = ref<BundleSummary[]>([]);
  const optionsLoading = ref(false);
  const optionsError = ref<string | null>(null);
  // REQ-056 D10: the approved ValuesRevisions the operation may bind to. It used to
  // be a free-text id, so the form could carry an id the server would refuse
  // (values_not_approved) with no way to see what was actually approved.
  const approvedRevisions = ref<ValuesRevision[]>([]);
  const revisionsLoading = ref(false);
  const revisionsError = ref<string | null>(null);
  const step = ref<'form' | 'confirm'>('form');
  const submitting = ref(false);
  const submitError = ref<OperationAPIError | null>(null);
  const createdOperationId = ref<string | null>(null);
  const idempotencyKey = ref<string | null>(null);
  const draftReady = ref(false);

  const selectedBundle = computed(
    () => availableBundles.value.find((bundle) => bundle.bundleId === fields.bundleId) ?? null,
  );
  const isEmpty = computed(
    () => !optionsLoading.value && !optionsError.value && availableBundles.value.length === 0,
  );

  function applyOperationType(operationType: OperationType): void {
    step.value = 'form';
    submitError.value = null;
    if (operationType === 'INSTALL') {
      fields.expectedCurrentRevision = null;
    } else if (operationType === 'ROLLBACK') {
      fields.patch = [];
      fields.valuesRevisionId = null;
    }
  }

  watch(() => fields.operationType, applyOperationType);

  watch(
    fields,
    () => {
      if (!draftReady.value || !releaseDefinitionId.value) return;
      const safePatch = fields.patch.filter((override) => override.kind !== 'LITERAL' || !isSecretPath(override.path));
      const draft: DraftPayload = {
        operationType: fields.operationType,
        bundleId: fields.bundleId,
        valuesRevisionId: fields.valuesRevisionId,
        patch: safePatch,
        targetRevision: fields.targetRevision,
      };
      sessionStorage.setItem(draftKey(releaseDefinitionId.value), JSON.stringify(draft));
    },
    { deep: true },
  );

  async function setScope(nextReleaseDefinitionId: string): Promise<void> {
    if (releaseDefinitionId.value === nextReleaseDefinitionId) return;
    const previousReleaseDefinitionId = releaseDefinitionId.value;
    if (previousReleaseDefinitionId) {
      sessionStorage.removeItem(draftKey(previousReleaseDefinitionId));
    }
    releaseDefinitionId.value = nextReleaseDefinitionId;
    resetTransient();
    restoreDraft();
    await loadOptions();
  }

  async function loadOptions(): Promise<void> {
    if (!releaseDefinitionId.value) return;
    optionsLoading.value = true;
    optionsError.value = null;
    try {
      const options = await loadOperationOptions(releaseDefinitionId.value);
      availableBundles.value = options.bundles;
    } catch (error) {
      optionsError.value = mapOperationError(error).message;
    } finally {
      optionsLoading.value = false;
    }
    // The revision list loads independently: a failure there must not hide the
    // bundles, and the empty selector still tells the operator why it is empty.
    await loadApprovedRevisions();
  }

  // TASK-280: one monotonic token per approved-revision request. Two loads overlap
  // whenever the route scope changes (or loadOptions is re-called) before the first
  // response lands, so a response is only allowed to touch state while it is still the
  // newest request for the scope it was issued for.
  let revisionsRequestSeq = 0;

  async function loadApprovedRevisions(): Promise<void> {
    const scope = releaseDefinitionId.value;
    if (!scope) return;
    const requestSeq = ++revisionsRequestSeq;
    // Both halves are needed: the token rejects an older response of a same-scope race,
    // while the scope snapshot rejects a response whose scope changed during the await
    // without a newer load (releaseDefinitionId is an exposed, writable ref).
    const isCurrentRequest = (): boolean => requestSeq === revisionsRequestSeq && scope === releaseDefinitionId.value;
    revisionsLoading.value = true;
    revisionsError.value = null;
    try {
      const revisions = await listApprovedValuesRevisions(scope);
      // A superseded or out-of-scope response must neither overwrite this scope's list
      // nor reconcile this scope's draft against the other scope's approved ids, which
      // would silently destroy a legitimate restored selection.
      if (!isCurrentRequest()) return;
      approvedRevisions.value = revisions;
      // Reconcile only once the real list is in hand. `approvedRevisions` is empty
      // while this await is in flight -- and stays empty after a failed load -- so
      // reconciling any earlier would throw away a legitimate restored draft on every
      // scope entry. The failure path deliberately keeps the draft value: the list is
      // unknown, not empty, and validate() already refuses to submit without a match.
      reconcileValuesRevision();
    } catch (error) {
      if (!isCurrentRequest()) return;
      approvedRevisions.value = [];
      revisionsError.value = mapOperationError(error).message;
    } finally {
      if (isCurrentRequest()) revisionsLoading.value = false;
    }
  }

  /*
   * REQ-056 D10 invariant: the pending ValuesRevision id must belong to the approved
   * list. A draft written by the old free-text form, or a revision that was
   * superseded after it was selected, used to survive the selector (which renders
   * blank/disabled for it) and reach CreateOperation, earning a server-side
   * revision_not_approved round trip. Clearing it here keeps the form's own state
   * consistent with what the selector shows; validate() is the second, always-on
   * guard that also covers ids written straight into the field.
   */
  function reconcileValuesRevision(): void {
    const revisionId = fields.valuesRevisionId;
    if (!revisionId) return;
    if (approvedRevisions.value.some((revision) => revision.id === revisionId)) return;
    fields.valuesRevisionId = null;
  }

  function setOperationType(operationType: OperationType): void {
    fields.operationType = operationType;
    applyOperationType(operationType);
  }

  function addPatch(): void {
    if (fields.operationType === 'ROLLBACK') return;
    fields.patch.push({ path: '', value: '', kind: 'LITERAL' });
  }

  function removePatch(index: number): void {
    fields.patch.splice(index, 1);
  }

  function validate(): OperationFormErrors {
    const errors: OperationFormErrors = {};
    // Canonical rollback (REQ-067 / AC-056-08) renders neither a bundle nor a patch and does
    // not require a values revision; it needs target_revision instead.
    if (fields.operationType !== 'ROLLBACK') {
      if (!fields.bundleId) errors.bundleId = '请选择制品';
      else if (!selectedBundle.value) errors.bundleId = '所选制品未通过验证';
      // Membership, not just non-emptiness (REQ-056 D10): the selector only offers
      // approved ids, so an id outside the list means a stale draft or a field write
      // and must fail here rather than at the server.
      const revisionId = fields.valuesRevisionId;
      if (!revisionId || revisionId.trim() === '') {
        errors.valuesRevisionId = '请选择已审批的配置版本';
      } else if (!approvedRevisions.value.some((revision) => revision.id === revisionId)) {
        errors.valuesRevisionId = '请选择已审批的配置版本';
      }
    }
    if (fields.operationType !== 'INSTALL' && (!fields.expectedCurrentRevision || fields.expectedCurrentRevision < 1)) {
      errors.expectedCurrentRevision = '无法确定当前 Revision';
    }
    if (fields.operationType === 'ROLLBACK') {
      const target = fields.targetRevision;
      if (target === null || target === undefined || `${target}`.trim() === '') {
        errors.targetRevision = '请填写回滚目标 Revision';
      } else if (!Number.isInteger(target)) {
        errors.targetRevision = '目标 Revision 必须是整数';
      } else if (target < 1) {
        errors.targetRevision = '目标 Revision 必须不小于 1';
      } else if (fields.expectedCurrentRevision !== null && target === fields.expectedCurrentRevision) {
        errors.targetRevision = '目标 Revision 不能等于当前 Revision';
      }
    }
    const paths = new Set<string>();
    for (const [index, override] of fields.patch.entries()) {
      if (!patchPathPattern.test(override.path)) {
        errors.patch = 'Patch 路径格式错误';
        errors.patchIndex = index;
        break;
      }
      if (paths.has(override.path)) {
        errors.patch = 'Patch 路径重复';
        errors.patchIndex = index;
        break;
      }
      paths.add(override.path);
      if (override.value.trim() === '') {
        errors.patch = '请填写覆盖值';
        errors.patchIndex = index;
        break;
      }
      if (override.kind === 'LITERAL' && isSecretPath(override.path)) {
        errors.patch = 'Secret 类字段必须使用 Secret 引用';
        errors.patchIndex = index;
        break;
      }
      if (override.path.toLowerCase().includes('image') && selectedBundle.value) {
        const allowed = selectedBundle.value.images.some(
          (image) => override.path === image.valuesPath || override.path.startsWith(`${image.valuesPath}.`),
        );
        if (!allowed) {
          errors.patch = 'Patch 引用了 Bundle 外镜像';
          errors.patchIndex = index;
          break;
        }
      }
    }
    return errors;
  }

  function openConfirmation(): OperationFormErrors {
    const errors = validate();
    if (Object.keys(errors).length === 0) step.value = 'confirm';
    return errors;
  }

  function cancelConfirmation(): void {
    if (!submitting.value) step.value = 'form';
  }

  async function submit(): Promise<string | null> {
    if (submitting.value || !releaseDefinitionId.value) return null;
    const errors = validate();
    if (Object.keys(errors).length > 0) return null;
    submitting.value = true;
    submitError.value = null;
    idempotencyKey.value ??= crypto.randomUUID();
    try {
      // ROLLBACK has its own RPC: dispatching it through CreateOperation would fail the
      // server's INSTALL/UPGRADE-only contract (TASK-153 negative control).
      const created =
        fields.operationType === 'ROLLBACK'
          ? await rollbackRelease({
              idempotencyKey: idempotencyKey.value,
              releaseDefinitionId: releaseDefinitionId.value,
              targetRevision: fields.targetRevision ?? 0,
              expectedCurrentRevision: fields.expectedCurrentRevision ?? 0,
              reason: 'rollback requested from the release console',
            })
          : await createOperation({
              idempotencyKey: idempotencyKey.value,
              releaseDefinitionId: releaseDefinitionId.value,
              operationType: fields.operationType,
              bundleId: fields.bundleId ?? undefined,
              expectedCurrentRevision:
                fields.operationType === 'INSTALL' ? undefined : (fields.expectedCurrentRevision ?? undefined),
              valuesRevisionId: fields.valuesRevisionId ?? '',
              patch: fields.patch,
            });
      createdOperationId.value = created.operationId;
      clearDraft();
      return created.operationId;
    } catch (error) {
      submitError.value = mapOperationError(error);
      return null;
    } finally {
      submitting.value = false;
    }
  }

  function clearDraft(): void {
    if (releaseDefinitionId.value) sessionStorage.removeItem(draftKey(releaseDefinitionId.value));
  }

  function restoreDraft(): void {
    draftReady.value = false;
    if (!releaseDefinitionId.value) return;
    try {
      const raw = sessionStorage.getItem(draftKey(releaseDefinitionId.value));
      if (raw) {
        const draft = JSON.parse(raw) as Partial<DraftPayload>;
        if (draft.operationType === 'INSTALL' || draft.operationType === 'UPGRADE' || draft.operationType === 'ROLLBACK') {
          fields.operationType = draft.operationType;
        }
        fields.bundleId = typeof draft.bundleId === 'string' ? draft.bundleId : null;
        fields.valuesRevisionId = typeof draft.valuesRevisionId === 'string' ? draft.valuesRevisionId : null;
        fields.targetRevision = typeof draft.targetRevision === 'number' ? draft.targetRevision : null;
        fields.patch = Array.isArray(draft.patch)
          ? draft.patch.filter((override) => override.kind !== 'LITERAL' || !isSecretPath(override.path))
          : [];
      }
    } catch {
      clearDraft();
    } finally {
      draftReady.value = true;
    }
  }

  function resetTransient(): void {
    availableBundles.value = [];
    optionsError.value = null;
    approvedRevisions.value = [];
    revisionsError.value = null;
    step.value = 'form';
    submitting.value = false;
    submitError.value = null;
    createdOperationId.value = null;
    idempotencyKey.value = null;
    fields.operationType = 'INSTALL';
    fields.bundleId = null;
    fields.valuesRevisionId = null;
    fields.patch = [];
    fields.expectedCurrentRevision = null;
    fields.targetRevision = null;
  }

  return {
    releaseDefinitionId,
    fields,
    availableBundles,
    optionsLoading,
    optionsError,
    approvedRevisions,
    revisionsLoading,
    revisionsError,
    step,
    submitting,
    submitError,
    createdOperationId,
    selectedBundle,
    isEmpty,
    setScope,
    loadOptions,
    setOperationType,
    addPatch,
    removePatch,
    validate,
    openConfirmation,
    cancelConfirmation,
    submit,
    clearDraft,
  };
});

function draftKey(releaseDefinitionId: string): string {
  return `op-draft:${releaseDefinitionId}`;
}

function isSecretPath(path: string): boolean {
  const lowerPath = path.toLowerCase();
  return lowerPath.includes('password') || lowerPath.includes('secret') || lowerPath.includes('token');
}
