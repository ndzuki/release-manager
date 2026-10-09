// Emergency change form store (plan v3 Step 3 / decisions D4/D6).
// Holds minimal source state; availability, confirmation readiness, mapping
// completeness and intent staleness are derived via computed. AbortControllers
// and the idempotency key live in module scope — never serialized to Pinia
// state and never persisted to browser storage (AC-058-15/16/17).
//
// Three actions are submittable and the request carries exactly one of them
// (REQ-081 single-action semantics; internal/orchestrator/emergency.go:1065):
// image (container + artifactRef), replicas (set_replicas ≥ 1) and approved
// annotations (entries + scope). `actionKind` is the selector, and the intent
// fingerprint covers the selected action's payload, so switching actions or
// editing their values mints a new idempotency key.
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
  type ExecuteEmergencyInput,
} from '@/connect/emergency-api';
import { mapEmergencyError, type EmergencyErrorDisplay } from '@/features/emergency/errors';
import {
  annotationMappingComplete,
  canonicalIntentJson,
  imageMappingComplete,
  replicasMappingComplete,
  validateAnnotationEntries,
  validateReason,
  validateReplicasChange,
  type AnnotationEntryDraft,
  type EmergencyIntentFields,
} from '@/features/emergency/validation';
import {
  annotationScopes,
  approvedAnnotationKeysForScope,
  availableEmergencyActions,
  workloadRefToWire,
  type CandidateArtifactDisplay,
  type ConvergencePolicy,
  type EmergencyActionKind,
  type EmergencyTargetDisplay,
  type WorkloadRefDisplay,
} from '@/features/emergency/model';

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
  execute?: (input: ExecuteEmergencyInput, signal: AbortSignal) => Promise<{ operationId: string; operationVersion: string }>;
  abortController?: () => AbortController;
  randomUUID?: () => string;
}

// Deterministic business rejections invalidate the frozen key: retrying the
// same content after a business rejection needs a fresh intent (AC-058-16/17).
// Transient failures (network, stale snapshot, offline operator) keep the key.
const KEY_INVALIDATING_CODES = new Set<string>([
  'idempotency_conflict',
  'artifact_not_trusted',
  'artifact_not_found',
  'no_candidate_artifact',
  'version_invalid',
  'locked_path',
  'promotion_not_supported',
  'release_busy',
  'permission_denied',
  'kill_switch_disabled',
  // Deterministic payload rejections for the replicas / annotations actions
  // (REQ-081, REQ-058): retrying unchanged content cannot succeed, so the next
  // confirm mints a fresh key rather than replaying the frozen intent.
  'invalid_replicas',
  'hpa_managed',
  'workload_kind_not_supported',
  'invalid_annotation_entries',
  'duplicate_annotation_key',
  'annotation_key_not_allowed',
  'annotation_scope_mismatch',
  'conflicting_change',
]);

/**
 * Server codes that belong to one action's field, so the message renders next
 * to the control instead of only in the submit summary (D7 double-track).
 * Codes outside both sets stay summary-level.
 */
const REPLICAS_ERROR_CODES = new Set<string>(['invalid_replicas', 'hpa_managed', 'workload_kind_not_supported']);
const ANNOTATION_ERROR_CODES = new Set<string>([
  'invalid_annotation_entries',
  'duplicate_annotation_key',
  'annotation_key_not_allowed',
  'annotation_scope_mismatch',
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
  const selectedContainer = ref('');
  const loadingArtifacts = ref(false);
  const artifacts = ref<CandidateArtifactDisplay[]>([]);
  const selectedArtifact = ref<CandidateArtifactDisplay | null>(null);

  const operationVersion = ref('');
  const reason = ref('');
  const convergencePolicy = ref<ConvergencePolicy>('REQUIRE_PROMOTION');

  // The selected action and its payload. Exactly one branch is submittable at a
  // time (server-side branch exclusivity), so the other branches' state is kept
  // for convenience but never reaches the wire while another action is selected.
  const actionKind = ref<EmergencyActionKind>('image');
  const replicasValue = ref<number | null>(null);
  const annotationScope = ref('');
  const annotationEntries = ref<AnnotationEntryDraft[]>([]);

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

  /** Actions the selected target currently advertises as available. */
  const availableActions = computed<EmergencyActionKind[]>(() =>
    selectedTargetDisplay.value ? availableEmergencyActions(selectedTargetDisplay.value) : [],
  );

  const selectedReplicasAction = computed(() => selectedTargetDisplay.value?.replicasAction ?? null);
  const replicasMax = computed(() => selectedReplicasAction.value?.maxEmergencyReplicas ?? 0);
  const replicasCurrent = computed(() => selectedReplicasAction.value?.currentReplicas ?? null);
  const replicasHpaManaged = computed(() => selectedReplicasAction.value?.hpaManaged ?? false);
  const replicasAvailable = computed(() => selectedReplicasAction.value?.availability.available ?? false);
  const replicasValidation = computed(() =>
    validateReplicasChange(replicasValue.value, replicasMax.value, replicasHpaManaged.value),
  );

  const annotationScopesAvailable = computed(() =>
    selectedTargetDisplay.value ? annotationScopes(selectedTargetDisplay.value) : [],
  );
  const approvedAnnotationKeys = computed(() =>
    selectedTargetDisplay.value ? approvedAnnotationKeysForScope(selectedTargetDisplay.value, annotationScope.value) : [],
  );
  const annotationValidation = computed(() =>
    validateAnnotationEntries(
      annotationEntries.value,
      selectedTargetDisplay.value ? approvedAnnotationKeys.value : undefined,
    ),
  );

  const imageMappingCompleteForSelection = computed(() => {
    const target = selectedTargetDisplay.value;
    if (!target || selectedContainer.value === '') return false;
    return imageMappingComplete(target, selectedContainer.value);
  });

  const replicasMappingCompleteForSelection = computed(() =>
    selectedTargetDisplay.value ? replicasMappingComplete(selectedTargetDisplay.value) : false,
  );

  const annotationsMappingCompleteForSelection = computed(() => {
    const target = selectedTargetDisplay.value;
    if (!target || annotationEntries.value.length === 0) return false;
    return annotationMappingComplete(target, annotationEntries.value.map((entry) => entry.key));
  });

  /**
   * Mapping completeness for the SELECTED action (AC-058-14). Image mappings
   * are keyed by field "image_digest" + container, replicas by field
   * "replicas", annotations by field == key.
   */
  const mappingComplete = computed(() => {
    switch (actionKind.value) {
      case 'replicas':
        return replicasMappingCompleteForSelection.value;
      case 'annotations':
        return annotationsMappingCompleteForSelection.value;
      default:
        return imageMappingCompleteForSelection.value;
    }
  });

  const requirePromotionAvailable = computed(() => mappingComplete.value);

  /** Effective policy: REQUIRE_PROMOTION degrades to REVERT when mapping is
   * incomplete (AC-058-14) — the UI can never submit REQUIRE_PROMOTION
   * without a full mapping. */
  const effectivePolicy = computed<ConvergencePolicy>(() =>
    requirePromotionAvailable.value ? convergencePolicy.value : 'REVERT_ON_NEXT_RECONCILE',
  );

  /** Whether the selected action's own payload is complete (before reason/policy). */
  const actionPayloadValid = computed(() => {
    switch (actionKind.value) {
      case 'replicas':
        return replicasAvailable.value && replicasValidation.value.valid;
      case 'annotations':
        return (
          availableActions.value.includes('annotations') &&
          annotationScope.value !== '' &&
          annotationValidation.value.valid
        );
      default:
        return selectedContainer.value !== '' && selectedArtifact.value !== null;
    }
  });

  /** Promotion lock paths for the selected action (AC-058-25 / AC-079-G9). */
  function targetLocksForSelection(): string[] {
    switch (actionKind.value) {
      case 'replicas':
        return replicasMappingCompleteForSelection.value
          ? selectedReplicasAction.value?.promotions.map((promotion) => promotion.valuesPath) ?? []
          : [];
      case 'annotations': {
        const target = selectedTargetDisplay.value;
        if (!target || !annotationsMappingCompleteForSelection.value) return [];
        const keys = new Set(annotationEntries.value.map((entry) => entry.key));
        return [
          ...new Set(
            target.annotationActions
              .filter((action) => action.scope === annotationScope.value && keys.has(action.key))
              .flatMap((action) => action.promotions.map((promotion) => promotion.valuesPath)),
          ),
        ];
      }
      default:
        return imageMappingCompleteForSelection.value
          ? selectedImageAction.value?.promotions.map((promotion) => promotion.valuesPath) ?? []
          : [];
    }
  }

  function buildIntentFields(): EmergencyIntentFields | null {
    if (!releaseDefinitionId.value || !selectedTarget.value) return null;
    const base = {
      releaseDefinitionId: releaseDefinitionId.value,
      workloadRef: workloadRefToWire(selectedTarget.value),
      operationVersion: operationVersion.value,
      convergenceStrategy: effectivePolicy.value,
      targetLocks: targetLocksForSelection(),
    };
    switch (actionKind.value) {
      case 'replicas':
        if (replicasValue.value === null) return null;
        return {
          ...base,
          actionKind: 'replicas',
          container: '',
          artifactRef: '',
          setReplicas: replicasValue.value,
          annotations: [],
          annotationScope: '',
        };
      case 'annotations':
        if (annotationScope.value === '' || annotationEntries.value.length === 0) return null;
        return {
          ...base,
          actionKind: 'annotations',
          container: '',
          artifactRef: '',
          setReplicas: 0,
          annotations: annotationEntries.value.map((entry) => ({ key: entry.key, value: entry.value })),
          annotationScope: annotationScope.value,
        };
      default:
        if (selectedContainer.value === '' || selectedArtifact.value === null) return null;
        return {
          ...base,
          actionKind: 'image',
          container: selectedContainer.value,
          artifactRef: selectedArtifact.value.id,
          setReplicas: 0,
          annotations: [],
          annotationScope: '',
        };
    }
  }

  const intentJson = computed(() => {
    const fields = buildIntentFields();
    return fields ? canonicalIntentJson(fields) : '';
  });

  const intentChanged = computed(
    () => confirmedIntent.value !== null && confirmedIntent.value.intentJson !== intentJson.value,
  );

  const canConfirm = computed(
    () => actionPayloadValid.value && reasonValid.value && !submitting.value && intentJson.value !== '',
  );

  /**
   * Field-level server errors (D7 double-track). The summary bar keeps showing
   * `submitError`; these route the stable code to the control that caused it.
   */
  const replicasError = computed(() => {
    const server = submitError.value;
    if (server && REPLICAS_ERROR_CODES.has(server.code)) return server.message;
    // An untouched field must not shout its "required" message before the user
    // has interacted with it.
    if (replicasValue.value === null) return null;
    return replicasValidation.value.valid ? null : replicasValidation.value.message;
  });

  const annotationError = computed(() => {
    const server = submitError.value;
    return server && ANNOTATION_ERROR_CODES.has(server.code) ? server.message : null;
  });

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
    actionKind.value = 'image';
    replicasValue.value = null;
    annotationScope.value = '';
    annotationEntries.value = [];
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
    selectedContainer.value = '';
    artifacts.value = [];
    selectedArtifact.value = null;
    // Per-target payload state never carries over, and the action resets to the
    // first one this target actually advertises (image when it has containers,
    // replicas otherwise — the selector's order).
    replicasValue.value = null;
    annotationEntries.value = [];
    annotationScope.value = annotationScopes(target)[0] ?? '';
    actionKind.value = availableEmergencyActions(target)[0] ?? 'image';
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

  /** Switching the action is an intent change: the frozen key must not replay a
   * request whose payload describes a different action (AC-058-16/17). */
  function setActionKind(next: EmergencyActionKind): void {
    if (actionKind.value === next) return;
    actionKind.value = next;
    confirmedIntent.value = null;
    submitError.value = null;
  }

  function setReplicas(value: number | null): void {
    replicasValue.value = value;
    confirmedIntent.value = null;
    submitError.value = null;
  }

  /**
   * Re-anchors every row to the new scope and drops rows whose key is not
   * approved for it: the server validates (key, scope) as a pair and refuses a
   * mismatch, so carrying a row over would freeze an unsubmittable intent.
   */
  function setAnnotationScope(scope: string): void {
    annotationScope.value = scope;
    const approved = selectedTargetDisplay.value
      ? approvedAnnotationKeysForScope(selectedTargetDisplay.value, scope)
      : [];
    const allowed = new Set(approved.map((entry) => entry.key));
    annotationEntries.value = annotationEntries.value
      .filter((entry) => allowed.has(entry.key))
      .map((entry) => ({ ...entry, scope }));
    confirmedIntent.value = null;
    submitError.value = null;
  }

  function setAnnotationEntries(entries: AnnotationEntryDraft[]): void {
    annotationEntries.value = entries;
    confirmedIntent.value = null;
    submitError.value = null;
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
    // Exactly one action reaches the wire: the discriminated union (see
    // connect/emergency-api.ts) makes a combined request unrepresentable, which
    // is what the server's branch exclusivity demands.
    const base = {
      releaseDefinitionId: fields.releaseDefinitionId,
      workloadRef: fields.workloadRef,
      operationVersion: fields.operationVersion,
      convergenceStrategy:
        fields.convergenceStrategy === 'REQUIRE_PROMOTION'
          ? ('REQUIRE_PROMOTION' as const)
          : ('REVERT_ON_NEXT_RECONCILE' as const),
      targetLocks: fields.targetLocks,
      idempotencyKey: intent.idempotencyKey,
    };
    const input: ExecuteEmergencyInput =
      fields.actionKind === 'replicas'
        ? { ...base, action: 'replicas', setReplicas: fields.setReplicas }
        : fields.actionKind === 'annotations'
          ? { ...base, action: 'annotations', annotations: fields.annotations, annotationScope: fields.annotationScope }
          : { ...base, action: 'image', container: fields.container, artifactRef: fields.artifactRef };
    try {
      const result = await execute(input, signal ?? abortController?.signal as AbortSignal);
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
      if (mapped.code === 'artifact_not_trusted') {
        // AC-058-11: the artifact was VERIFIED when it was listed, but its trust
        // was revoked before submit. The selection is now invalid, so clear it
        // and reload the candidates rather than let the user retry a stale
        // choice. loadArtifactsForSelection already resets the selection.
        await loadArtifactsForSelection();
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
    selectedContainer.value = '';
    loadingArtifacts.value = false;
    artifacts.value = [];
    selectedArtifact.value = null;
    operationVersion.value = '';
    reason.value = '';
    convergencePolicy.value = 'REQUIRE_PROMOTION';
    actionKind.value = 'image';
    replicasValue.value = null;
    annotationScope.value = '';
    annotationEntries.value = [];
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
    selectedContainer,
    loadingArtifacts,
    artifacts,
    selectedArtifact,
    operationVersion,
    reason,
    convergencePolicy,
    actionKind,
    replicasValue,
    annotationScope,
    annotationEntries,
    confirmedIntent,
    confirmOpen,
    submitting,
    submitError,
    lastAcceptedAt,
    selectedTargetDisplay,
    selectedImageAction,
    selectedReplicasAction,
    availableActions,
    replicasMax,
    replicasCurrent,
    replicasHpaManaged,
    replicasAvailable,
    replicasValidation,
    replicasError,
    annotationScopesAvailable,
    approvedAnnotationKeys,
    annotationValidation,
    annotationError,
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
    setActionKind,
    setReplicas,
    setAnnotationScope,
    setAnnotationEntries,
    setReason,
    setConvergencePolicy,
    openConfirm,
    closeConfirm,
    setRiskAccepted,
    submit,
    reset,
  };
});
