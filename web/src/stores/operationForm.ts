import { defineStore } from 'pinia';
import { computed, reactive, ref, watch } from 'vue';
import {
  createOperation,
  loadOperationOptions,
  mapOperationError,
  type OperationAPIError,
} from '@/connect/operation-api';
import type {
  BundleSummary,
  OperationType,
  PatchOverride,
} from '@/types/operation';

interface DraftPayload {
  operationType: OperationType;
  bundleId: string | null;
  valuesRevisionId: string | null;
  patch: PatchOverride[];
}

interface OperationFormFields {
  operationType: OperationType;
  bundleId: string | null;
  valuesRevisionId: string | null;
  patch: PatchOverride[];
  expectedCurrentRevision: number | null;
}

export interface OperationFormErrors {
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
  });
  const availableBundles = ref<BundleSummary[]>([]);
  const optionsLoading = ref(false);
  const optionsError = ref<string | null>(null);
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
    if (!fields.bundleId) errors.bundleId = '请选择制品';
    else if (!selectedBundle.value) errors.bundleId = '所选制品未通过验证';
    if (!fields.valuesRevisionId || fields.valuesRevisionId.trim() === '') {
      errors.valuesRevisionId = '请填写已审批的配置版本 ID';
    }
    if (fields.operationType !== 'INSTALL' && (!fields.expectedCurrentRevision || fields.expectedCurrentRevision < 1)) {
      errors.expectedCurrentRevision = '无法确定当前 Revision';
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
      const created = await createOperation({
        idempotencyKey: idempotencyKey.value,
        releaseDefinitionId: releaseDefinitionId.value,
        operationType: fields.operationType,
        bundleId: fields.bundleId ?? undefined,
        expectedCurrentRevision: fields.operationType === 'INSTALL' ? undefined : fields.expectedCurrentRevision ?? undefined,
        valuesRevisionId: fields.valuesRevisionId ?? '',
        patch: fields.operationType === 'ROLLBACK' ? [] : fields.patch,
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
  }

  return {
    releaseDefinitionId,
    fields,
    availableBundles,
    optionsLoading,
    optionsError,
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
