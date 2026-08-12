import { computed, ref, shallowRef } from 'vue';
import { defineStore } from 'pinia';
import {
  createEnrollmentToken,
  getEnrollmentTokenStatus,
  getOperator,
  listOperators,
  mapOperatorError,
  revokeOperator as revokeOperatorRpc,
  revokePendingEnrollmentToken,
} from '@/connect/operator-api';
import { validateEnrollmentForm, validateRevokeReason } from '@/utils/operator-validation';
import type {
  EnrollmentFormInput,
  EnrollmentTokenResult,
  OperatorApiError,
  OperatorDetail,
  OperatorListFilters,
  OperatorSummary,
  PendingTokenMetadata,
} from '@/types/operator';

export const useOperatorStore = defineStore('operator', () => {
  const operators = ref<OperatorSummary[]>([]);
  const current = ref<OperatorDetail | null>(null);
  const pending = ref<PendingTokenMetadata | null>(null);
  const filters = ref<OperatorListFilters>({ lifecycleStatus: null, sessionStatus: null });
  const nextPageToken = shallowRef<string | null>(null);
  const totalCount = shallowRef(0);
  const heartbeatIntervalSeconds = shallowRef<number | null>(null);
  const loading = shallowRef(false);
  const loadingMore = shallowRef(false);
  const saving = shallowRef(false);
  const error = ref<OperatorApiError | null>(null);
  const forbidden = shallowRef(false);
  const notFound = shallowRef(false);
  const enrollmentForm = ref<EnrollmentFormInput>({ operatorName: '', ttlMinutes: 0 });

  const hasOperators = computed(() => operators.value.length > 0);

  async function loadList(customerId: string, clusterId: string, append = false): Promise<boolean> {
    if (append) loadingMore.value = true;
    else loading.value = true;
    error.value = null;
    forbidden.value = false;
    notFound.value = false;
    try {
      const page = await listOperators(customerId, clusterId, filters.value, append ? nextPageToken.value ?? '' : '');
      operators.value = append ? [...operators.value, ...page.operators] : page.operators;
      nextPageToken.value = page.nextPageToken;
      totalCount.value = page.totalCount;
      heartbeatIntervalSeconds.value = page.heartbeatIntervalSeconds;
      return true;
    } catch (cause) {
      applyError(cause);
      return false;
    } finally {
      loading.value = false;
      loadingMore.value = false;
    }
  }

  async function loadDetail(customerId: string, clusterId: string, operatorId: string): Promise<boolean> {
    loading.value = true;
    error.value = null;
    forbidden.value = false;
    notFound.value = false;
    try {
      const result = await getOperator(customerId, clusterId, operatorId);
      current.value = result.operator;
      heartbeatIntervalSeconds.value = result.heartbeatIntervalSeconds;
      return true;
    } catch (cause) {
      applyError(cause);
      return false;
    } finally {
      loading.value = false;
    }
  }

  async function loadPending(customerId: string, clusterId: string): Promise<void> {
    try {
      pending.value = await getEnrollmentTokenStatus(customerId, clusterId);
    } catch (cause) {
      applyError(cause);
    }
  }

  async function generateToken(
    customerId: string,
    clusterId: string,
    replacePending = false,
  ): Promise<EnrollmentTokenResult | null> {
    const validation = validateEnrollmentForm(enrollmentForm.value);
    if (!validation.valid) {
      error.value = {
        code: 'client_validation',
        message: 'Fix the highlighted fields before generating a token.',
        fieldViolations: validation.violations,
        retryable: false,
      };
      return null;
    }
    saving.value = true;
    error.value = null;
    try {
      const result = await createEnrollmentToken(customerId, clusterId, enrollmentForm.value, replacePending);
      pending.value = {
        state: 'pending',
        createdAt: new Date().toISOString(),
        expiresAt: result.expiresAt,
        createdByDisplayName: null,
      };
      return result;
    } catch (cause) {
      const mapped = applyError(cause);
      if (mapped.code === 'pending_token_exists') await loadPending(customerId, clusterId);
      return null;
    } finally {
      saving.value = false;
    }
  }

  async function discardPending(customerId: string, clusterId: string): Promise<boolean> {
    saving.value = true;
    error.value = null;
    try {
      await revokePendingEnrollmentToken(customerId, clusterId);
      pending.value = { state: 'none', createdAt: null, expiresAt: null, createdByDisplayName: null };
      return true;
    } catch (cause) {
      applyError(cause);
      return false;
    } finally {
      saving.value = false;
    }
  }

  async function revokeOperator(customerId: string, clusterId: string, operatorId: string, reason: string): Promise<boolean> {
    const violation = validateRevokeReason(reason);
    if (violation) {
      error.value = {
        code: 'client_validation',
        message: violation.description,
        fieldViolations: [violation],
        retryable: false,
      };
      return false;
    }
    saving.value = true;
    error.value = null;
    try {
      const result = await revokeOperatorRpc(customerId, clusterId, operatorId, reason);
      if (current.value?.id === operatorId) {
        const revokeReason = result.changed ? reason.trim() : current.value.revokeReason;
        current.value = { ...current.value, ...result.operator, revokeReason };
      }
      const index = operators.value.findIndex((operator) => operator.id === operatorId);
      if (index >= 0) operators.value[index] = result.operator;
      return true;
    } catch (cause) {
      applyError(cause);
      return false;
    } finally {
      saving.value = false;
    }
  }

  function resetListState(): void {
    operators.value = [];
    nextPageToken.value = null;
    totalCount.value = 0;
    heartbeatIntervalSeconds.value = null;
    error.value = null;
    forbidden.value = false;
    notFound.value = false;
  }

  function resetDetailState(): void {
    current.value = null;
    heartbeatIntervalSeconds.value = null;
    error.value = null;
    forbidden.value = false;
    notFound.value = false;
  }

  function resetEnrollmentState(): void {
    pending.value = null;
    resetEnrollmentForm();
    forbidden.value = false;
    notFound.value = false;
  }

  function setFilters(next: OperatorListFilters): void {
    filters.value = next;
    nextPageToken.value = null;
  }

  function resetEnrollmentForm(): void {
    enrollmentForm.value = { operatorName: '', ttlMinutes: 0 };
    error.value = null;
  }

  function applyError(cause: unknown): OperatorApiError {
    const mapped = mapOperatorError(cause);
    error.value = mapped;
    forbidden.value = mapped.code === 'permission_denied';
    notFound.value = mapped.code === 'not_found' || mapped.code === 'operator_not_found' || mapped.code === 'cluster_not_found';
    return mapped;
  }

  return {
    operators,
    current,
    pending,
    filters,
    nextPageToken,
    totalCount,
    heartbeatIntervalSeconds,
    loading,
    loadingMore,
    saving,
    error,
    forbidden,
    notFound,
    enrollmentForm,
    hasOperators,
    loadList,
    loadDetail,
    loadPending,
    generateToken,
    discardPending,
    revokeOperator,
    setFilters,
    resetEnrollmentForm,
    resetListState,
    resetDetailState,
    resetEnrollmentState,
  };
});
