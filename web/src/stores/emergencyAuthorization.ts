// Scoped Authorization Snapshot store (plan v3 Step 3 / decision D1).
// Hides the fail-closed capability projection: missing snapshot, stale
// policy or a missing kill-switch value all resolve to "no access" — the web
// never derives capabilities from role strings (ADR-006; REQ-033/049 seam).
//
// Per uncategorized/TASK-059-pitfall-2026-08-12-pinia-setup-store-p.md:
// source fields are assigned directly (never nested $patch) so computeds
// re-evaluate reliably.
import { defineStore } from 'pinia';
import { computed, ref } from 'vue';
import {
  getAuthorizationSnapshot,
  type EmergencyAuthorizationProjection,
} from '@/connect/emergency-api';
import { mapEmergencyError } from '@/features/emergency/errors';

export type RouteGate = 'loading' | 'allowed' | 'forbidden' | 'not_found' | 'unauthenticated';

export interface EmergencyAuthorizationOptions {
  /** Test seam: replace the snapshot loader. */
  loadSnapshot?: (
    organizationId: string,
    customerId: string,
    signal: AbortSignal,
  ) => Promise<EmergencyAuthorizationProjection>;
}

export const useEmergencyAuthorizationStore = defineStore('emergencyAuthorization', () => {
  const scopeKey = ref('');
  const snapshot = ref<EmergencyAuthorizationProjection | null>(null);
  const loading = ref(false);
  const error = ref<string | null>(null);
  const options = ref<EmergencyAuthorizationOptions>({});

  // Scope fencing: only the latest load may write state.
  let generation = 0;
  let abortController: AbortController | null = null;

  const featureEnabled = computed(() => snapshot.value?.emergencyChangeEnabled === true);
  const canExecuteEmergency = computed(() => snapshot.value?.canExecuteEmergency === true);
  const canCreateValuesRevision = computed(() => snapshot.value?.canCreateValuesRevision === true);
  const canApproveValuesRevision = computed(() => snapshot.value?.canApproveValuesRevision === true);
  // Backend projects Checkpoint == SourceVersion and Fresh == sourceVersion > 0,
  // so a fresh snapshot is by construction checkpoint-caught-up.
  const writeAllowed = computed(() => snapshot.value?.fresh === true);

  function configure(next: EmergencyAuthorizationOptions): void {
    options.value = { ...options.value, ...next };
  }

  /**
   * Resolves the route gate for a scoped page:
   * - no snapshot yet → 'loading'
   * - scope/binding missing → 'not_found' (404)
   * - feature off (kill switch, fail closed) → 'not_found' (404)
   * - missing capability → 'forbidden' (403)
   * - otherwise → 'allowed'
   */
  function gateFor(capability: 'execute' | 'createValues' | 'approveValues' | 'none'): RouteGate {
    if (!snapshot.value) return loading.value || error.value ? 'loading' : 'loading';
    if (!snapshot.value.bindingActive || !snapshot.value.customerActive) return 'not_found';
    // The kill switch only closes the NEW emergency entry; convergence and
    // existing operation paths stay reachable (AC-058-05).
    if (capability === 'execute' && !featureEnabled.value) return 'not_found';
    if (capability === 'execute' && !canExecuteEmergency.value) return 'forbidden';
    if (capability === 'createValues' && !canCreateValuesRevision.value) return 'forbidden';
    if (capability === 'approveValues' && !canApproveValuesRevision.value) return 'forbidden';
    return 'allowed';
  }

  async function load(organizationId: string, customerId: string, signal?: AbortSignal): Promise<void> {
    abortController?.abort();
    const controller = new AbortController();
    abortController = controller;
    const captured = ++generation;
    const nextScopeKey = `${organizationId}:${customerId}`;
    scopeKey.value = nextScopeKey;
    loading.value = true;
    error.value = null;
    try {
      const loader = options.value.loadSnapshot ?? getAuthorizationSnapshot;
      const next = await loader(organizationId, customerId, signal ?? controller.signal);
      if (captured !== generation || scopeKey.value !== nextScopeKey) return;
      snapshot.value = next;
    } catch (cause) {
      if (captured !== generation || scopeKey.value !== nextScopeKey) return;
      if ((signal ?? controller.signal).aborted) return;
      error.value = mapEmergencyError(cause).message;
    } finally {
      if (captured === generation) loading.value = false;
    }
  }

  function reset(): void {
    generation++;
    abortController?.abort();
    abortController = null;
    scopeKey.value = '';
    snapshot.value = null;
    loading.value = false;
    error.value = null;
  }

  return {
    scopeKey,
    snapshot,
    loading,
    error,
    featureEnabled,
    canExecuteEmergency,
    canCreateValuesRevision,
    canApproveValuesRevision,
    writeAllowed,
    configure,
    gateFor,
    load,
    reset,
  };
});
