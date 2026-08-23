// Emergency change form store (plan v3 Step 3 / decisions D4/D6).
// Holds minimal source state; availability, confirmation readiness, mapping
// completeness and intent staleness are derived via computed. AbortControllers
// and the idempotency key live in module scope — never serialized to Pinia
// state and never persisted to browser storage (AC-058-15/16/17).
//
// Contract divergence (recorded in the TASK record): the canonical
// ExecuteEmergencyChange only carries the image action (container +
// artifact_ref). Replicas/annotation actions surface with their target
// availability but stay non-executable until upstream extends the contract.
//
// Per uncategorized/TASK-059-pitfall-2026-08-12-pinia-setup-store-p.md:
// assign source fields directly — never nested $patch merges.
import { defineStore } from 'pinia';
import { computed, ref } from 'vue';
import {
  checkEmergencyConflict,
  executeEmergencyChange,
  listCandidateArtifacts,
  listEmergencyTargets,
  type EmergencyConflictDisplay,
} from '@/connect/emergency-api';
import { mapEmergencyError, type EmergencyErrorDisplay } from '@/features/emergency/errors';
import {
  canonicalIntentJson,
  imageMappingComplete,
  validateReason,
  type EmergencyIntentFields,
} from '@/features/emergency/validation';
import type {
  CandidateArtifactDisplay,
  ConvergencePolicy,
  EmergencyTargetDisplay,
  WorkloadRefDisplay,
} from '@/features/emergency/model';
import { workloadRefToWire } from '@/features/emergency/model';

export interface ConfirmedEmergencyIntent {
  intentJson: string;
  idempotencyKey: string;
  riskAccepted: boolean;
}

export interface EmergencyChangeOptions {
  /** Test seams: replace RPC loaders/executor and identity generators. */
  loadConflict?: (releaseDefinitionId: string, signal: AbortSignal) => Promise<EmergencyConflictDisplay>;
  loadTargets?: (releaseDefinitionId: string, signal: AbortSignal) => Promise<EmergencyTargetDisplay[]>;
  loadArtifacts?: (
    input: { organizationId: string; releaseDefinitionId: string; workloadRef: string; container: string; operationVersion: string },
    signal: AbortSignal,
  ) => Promise<CandidateArtifactDisplay[]>;
  execute?: (input: Parameters<typeof executeEmergencyChange>[0], signal: AbortSignal) => Promise<{ operationId: string; operationVersion: string }>;
  abortController?: () => AbortController;
  randomUUID?: () => string;
}

// Deterministic business rejections invalidate the frozen key: retrying the
// same content after a business rejection needs a fresh intent (AC-058-16/17).
// Transient failures (network, stale snapshot, offline operator) keep the key.
const KEY_INVALIDATING_CODES = new Set<string>([
  'idempotency_conflict',
  'target_changed',
  'artifact_not_trusted',
  'artifact_not_found',
  'no_candidate_artifact',
  'version_invalid',
  'locked_path',
  'promotion_not_supported',
  'release_busy',
  'permission_denied',
  'kill_switch_disabled',
]);

function defaultAbortController(): AbortController {
  return new AbortController();
}

function defaultRandomUUID(): string {
  return crypto.randomUUID();
}

export const useEmergencyChangeStore = defineStore('emergencyChange', () => {
  const scopeKey = ref('');
  const releaseDefinitionId = ref('');
  const organizationId = ref('');
  const customerId = ref('');

  const checkingConflict = ref(false);
  const conflict = ref<EmergencyConflictDisplay | null>(null);
  const loadingTargets = ref(false);
  const targets = ref<EmergencyTargetDisplay[]>([]);
  const loadError = ref<EmergencyErrorDisplay | null>(null);

  const selectedTarget = ref<WorkloadRefDisplay | null>(null);
  const selectedOpType = ref<'SET_CONTAINER_IMAGE' | 'SET_REPLICAS' | 'SET_APPROVED_ANNOTATION' | null>(null);
  const selectedContainer = ref('');
  const loadingArtifacts = ref(false);
  const artifacts = ref<CandidateArtifactDisplay[]>([]);
  const selectedArtifact = ref<CandidateArtifactDisplay | null>(null);

  const operationVersion = ref('');
  const reason = ref('');
  const convergencePolicy = ref<ConvergencePolicy>('REQUIRE_PROMOTION');

  const confirmedIntent = ref<ConfirmedEmergencyIntent | null>(null);
  const confirmOpen = ref(false);
  const submitting = ref(false);
  const submitError = ref<EmergencyErrorDisplay | null>(null);

  const options = ref<EmergencyChangeOptions>({});
  const lastAcceptedAt = ref<string | null>(null);

  // Scope fencing + abort handles live outside Pinia state: scopeGeneration
  // fences the loadScope sequence; artifactRequest fences the container →
  // artifact cascade so a stale artifact response never writes a new
  // selection (AC-058-10).
  let generation = 0;
  let artifactRequest = 0;
  let abortController: AbortController | null = null;
  let artifactController: AbortController | null = null;

  const selectedTargetDisplay = computed(
    () => targets.value.find((target) => target.workloadRef.uid === selectedTarget.value?.uid) ?? null,
  );

  const selectedImageAction = computed(() =>
    selectedTargetDisplay.value?.imageActions.find((action) => action.container === selectedContainer.value) ?? null,
  );

  const reasonValid = computed(() => validateReason(reason.value).valid);

  const mappingComplete = computed(() => {
    const target = selectedTargetDisplay.value;
    if (!target || selectedContainer.value === '') return false;
    return imageMappingComplete(target, selectedContainer.value);
  });

  const requirePromotionAvailable = computed(() => mappingComplete.value);

  /** Effective policy: REQUIRE_PROMOTION degrades to REVERT when mapping is
   * incomplete (AC-058-14) — the UI can never submit REQUIRE_PROMOTION
   * without a full mapping. */
  const effectivePolicy = computed<ConvergencePolicy>(() =>
    requirePromotionAvailable.value ? convergencePolicy.value : 'REVERT_ON_NEXT_RECONCILE',
  );

  function buildIntentFields(): EmergencyIntentFields | null {
    if (!releaseDefinitionId.value || !selectedTarget.value || !selectedContainer.value || !selectedArtifact.value) {
      return null;
    }
    return {
      releaseDefinitionId: releaseDefinitionId.value,
      workloadRef: workloadRefToWire(selectedTarget.value),
      container: selectedContainer.value,
      operationVersion: operationVersion.value,
      artifactRef: selectedArtifact.value.id,
      convergenceStrategy: effectivePolicy.value,
      targetLocks: mappingComplete.value ? selectedImageAction.value?.promotions.map((p) => p.valuesPath) ?? [] : [],
    };
  }

  const intentJson = computed(() => {
    const fields = buildIntentFields();
    return fields ? canonicalIntentJson(fields) : '';
  });

  const intentChanged = computed(
    () => confirmedIntent.value !== null && confirmedIntent.value.intentJson !== intentJson.value,
  );

  const canConfirm = computed(
    () =>
      selectedContainer.value !== '' &&
      selectedArtifact.value !== null &&
      reasonValid.value &&
      !submitting.value &&
      intentJson.value !== '',
  );

  function configure(next: EmergencyChangeOptions): void {
    options.value = { ...options.value, ...next };
  }

  function signalOrOwn(signal: AbortSignal | undefined, controller: AbortController): AbortSignal {
    return signal ?? controller.signal;
  }

  async function loadScope(
    input: { releaseDefinitionId: string; organizationId: string; customerId: string },
    signal?: AbortSignal,
  ): Promise<void> {
    abortController?.abort();
    const controller = options.value.abortController ? options.value.abortController() : defaultAbortController();
    abortController = controller;
    const captured = ++generation;
    const nextScopeKey = `${input.organizationId}:${input.customerId}:${input.releaseDefinitionId}`;

    scopeKey.value = nextScopeKey;
    releaseDefinitionId.value = input.releaseDefinitionId;
    organizationId.value = input.organizationId;
    customerId.value = input.customerId;
    conflict.value = null;
    targets.value = [];
    selectedTarget.value = null;
    selectedContainer.value = '';
    artifacts.value = [];
    selectedArtifact.value = null;
    operationVersion.value = '';
    confirmedIntent.value = null;
    confirmOpen.value = false;
    submitting.value = false;
    submitError.value = null;
    loadError.value = null;

    const loadConflict = options.value.loadConflict ?? checkEmergencyConflict;
    const loadTargets = options.value.loadTargets ?? listEmergencyTargets;

    try {
      checkingConflict.value = true;
      const conflictResult = await loadConflict(input.releaseDefinitionId, signalOrOwn(signal, controller));
      if (captured !== generation || scopeKey.value !== nextScopeKey) return;
      conflict.value = conflictResult;
      if (conflictResult.hasConflict) return;

      loadingTargets.value = true;
      const targetList = await loadTargets(input.releaseDefinitionId, signalOrOwn(signal, controller));
      if (captured !== generation || scopeKey.value !== nextScopeKey) return;
      targets.value = targetList;
      loadingTargets.value = false;

      // Auto-select the only executable action when a single target exists.
      if (targetList.length === 1) {
        selectTarget(targetList[0].workloadRef);
      }
    } catch (cause) {
      if (captured !== generation || scopeKey.value !== nextScopeKey) return;
      if (signalOrOwn(signal, controller).aborted) return;
      loadError.value = mapEmergencyError(cause);
    } finally {
      if (captured === generation) {
        checkingConflict.value = false;
        loadingTargets.value = false;
      }
    }
  }

  function selectTarget(refKey: WorkloadRefDisplay): void {
    const target = targets.value.find((candidate) => candidate.workloadRef.uid === refKey.uid);
    if (!target) return;
    if (selectedTarget.value?.uid === refKey.uid) return;
    selectedTarget.value = target.workloadRef;
    selectedOpType.value = 'SET_CONTAINER_IMAGE';
    selectedContainer.value = '';
    artifacts.value = [];
    selectedArtifact.value = null;
    confirmedIntent.value = null;
    void loadArtifactsForSelection();
  }

  async function loadArtifactsForSelection(signal?: AbortSignal): Promise<void> {
    const target = selectedTarget.value;
    if (!target || selectedContainer.value === '') return;
    artifactController?.abort();
    const controller = options.value.abortController ? options.value.abortController() : defaultAbortController();
    artifactController = controller;
    const captured = ++artifactRequest;
    const loadArtifacts = options.value.loadArtifacts ?? listCandidateArtifacts;

    loadingArtifacts.value = true;
    try {
      const next = await loadArtifacts(
        {
          organizationId: organizationId.value,
          releaseDefinitionId: releaseDefinitionId.value,
          workloadRef: workloadRefToWire(target),
          container: selectedContainer.value,
          operationVersion: operationVersion.value,
        },
        signalOrOwn(signal, controller),
      );
      if (captured !== artifactRequest) return;
      // Cascade reset: switching container invalidates the previous artifact
      // selection (AC-058-10).
      artifacts.value = next;
      selectedArtifact.value = null;
      confirmedIntent.value = null;
    } catch (cause) {
      if (captured !== artifactRequest) return;
      if (signalOrOwn(signal, controller).aborted) return;
      loadError.value = mapEmergencyError(cause);
    } finally {
      if (captured === artifactRequest) loadingArtifacts.value = false;
    }
  }

  function selectContainer(container: string): void {
    if (selectedContainer.value === container) return;
    selectedContainer.value = container;
    artifacts.value = [];
    selectedArtifact.value = null;
    confirmedIntent.value = null;
    void loadArtifactsForSelection();
  }

  function selectArtifact(artifact: CandidateArtifactDisplay): void {
    selectedArtifact.value = artifact;
    confirmedIntent.value = null;
  }

  function setReason(next: string): void {
    reason.value = next;
    confirmedIntent.value = null;
  }

  function setConvergencePolicy(next: ConvergencePolicy): void {
    convergencePolicy.value = next;
    confirmedIntent.value = null;
  }

  /**
   * Freezes the current intent: snapshots the canonical JSON and binds an
   * idempotency key. Reopening the dialog for the SAME intent reuses the key;
   * any hash input change (container, artifact, reason, policy, scope) already
   * cleared confirmedIntent above, so a fresh key is generated (AC-058-15/17).
   */
  function openConfirm(): void {
    if (!canConfirm.value) return;
    const json = intentJson.value;
    if (confirmedIntent.value && confirmedIntent.value.intentJson === json) {
      confirmOpen.value = true;
      return;
    }
    const randomUUID = options.value.randomUUID ?? defaultRandomUUID;
    confirmedIntent.value = {
      intentJson: json,
      idempotencyKey: randomUUID(),
      riskAccepted: false,
    };
    confirmOpen.value = true;
    submitError.value = null;
  }

  function closeConfirm(): void {
    // Closing keeps the form and the frozen snapshot (AC-058-17).
    confirmOpen.value = false;
  }

  function setRiskAccepted(accepted: boolean): void {
    if (confirmedIntent.value) confirmedIntent.value.riskAccepted = accepted;
  }

  const riskAccepted = computed(() => confirmedIntent.value?.riskAccepted === true);

  async function submit(signal?: AbortSignal): Promise<{ operationId: string } | null> {
    const intent = confirmedIntent.value;
    const fields = buildIntentFields();
    if (!intent || !fields || intent.intentJson !== intentJson.value || !intent.riskAccepted) {
      return null;
    }
    if (submitting.value) return null;
    submitting.value = true;
    submitError.value = null;
    const execute = options.value.execute ?? executeEmergencyChange;
    const captured = generation;
    try {
      const result = await execute(
        {
          releaseDefinitionId: fields.releaseDefinitionId,
          workloadRef: fields.workloadRef,
          container: fields.container,
          operationVersion: fields.operationVersion,
          artifactRef: fields.artifactRef,
          convergenceStrategy: fields.convergenceStrategy === 'REQUIRE_PROMOTION' ? 'REQUIRE_PROMOTION' : 'REVERT_ON_NEXT_RECONCILE',
          targetLocks: fields.targetLocks,
          idempotencyKey: intent.idempotencyKey,
        },
        signal ?? abortController?.signal as AbortSignal,
      );
      if (captured !== generation) return null;
      lastAcceptedAt.value = new Date().toISOString();
      operationVersion.value = result.operationVersion;
      return { operationId: result.operationId };
    } catch (cause) {
      if (captured !== generation) return null;
      if ((signal ?? abortController?.signal)?.aborted) return null;
      const mapped = mapEmergencyError(cause);
      submitError.value = mapped;
      if (KEY_INVALIDATING_CODES.has(mapped.code)) {
        // Deterministic rejection: the next confirm must mint a new key
        // (AC-058-16/17); transient errors keep the key for retry reuse.
        confirmedIntent.value = null;
      }
      return null;
    } finally {
      if (captured === generation) submitting.value = false;
    }
  }

  function reset(): void {
    generation++;
    artifactRequest++;
    abortController?.abort();
    abortController = null;
    artifactController?.abort();
    artifactController = null;
    scopeKey.value = '';
    releaseDefinitionId.value = '';
    organizationId.value = '';
    customerId.value = '';
    checkingConflict.value = false;
    conflict.value = null;
    loadingTargets.value = false;
    targets.value = [];
    loadError.value = null;
    selectedTarget.value = null;
    selectedOpType.value = null;
    selectedContainer.value = '';
    loadingArtifacts.value = false;
    artifacts.value = [];
    selectedArtifact.value = null;
    operationVersion.value = '';
    reason.value = '';
    convergencePolicy.value = 'REQUIRE_PROMOTION';
    confirmedIntent.value = null;
    confirmOpen.value = false;
    submitting.value = false;
    submitError.value = null;
    lastAcceptedAt.value = null;
  }

  return {
    scopeKey,
    releaseDefinitionId,
    organizationId,
    customerId,
    checkingConflict,
    conflict,
    loadingTargets,
    targets,
    loadError,
    selectedTarget,
    selectedOpType,
    selectedContainer,
    loadingArtifacts,
    artifacts,
    selectedArtifact,
    operationVersion,
    reason,
    convergencePolicy,
    confirmedIntent,
    confirmOpen,
    submitting,
    submitError,
    lastAcceptedAt,
    selectedTargetDisplay,
    selectedImageAction,
    reasonValid,
    mappingComplete,
    requirePromotionAvailable,
    effectivePolicy,
    intentJson,
    intentChanged,
    canConfirm,
    riskAccepted,
    configure,
    loadScope,
    selectTarget,
    selectContainer,
    selectArtifact,
    setReason,
    setConvergencePolicy,
    openConfirm,
    closeConfirm,
    setRiskAccepted,
    submit,
    reset,
  };
});
