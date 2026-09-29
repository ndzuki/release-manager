import { computed, ref } from 'vue';
import { defineStore } from 'pinia';
import {
  mapCleanupError,
  runCleanup,
  unarchiveBundle,
  type CleanupFailure,
  type CleanupResult,
  type UnarchiveResult,
} from '@/connect/cleanup-api';

/*
 * Artifact lifecycle state (REQ-069, A7).
 *
 * GC is irreversible and its success response still carries non-fatal per-phase
 * errors, so the store keeps the full result (counters + errors) and surfaces both.
 *
 * The idempotency key is OWNED BY THE CALLER, not generated here: the page keeps one
 * key per attempt so that its retry button reuses it — the server then either replays
 * the recorded run or (outside the key table) answers `cleanup_already_requested`,
 * which is the honest outcome. Requesting a NEW run is what rotates the key.
 */
export const useArtifactLifecycleStore = defineStore('artifactLifecycle', () => {
  const running = ref(false);
  const failure = ref<CleanupFailure | null>(null);
  const result = ref<CleanupResult | null>(null);
  const notice = ref('');

  const restoring = ref(false);
  const restoreFailure = ref<CleanupFailure | null>(null);
  const restoreResult = ref<UnarchiveResult | null>(null);

  const hasProblems = computed(() => (result.value?.errors.length ?? 0) > 0);

  async function clean(idempotencyKey: string): Promise<boolean> {
    running.value = true;
    failure.value = null;
    result.value = null;
    notice.value = '';
    try {
      result.value = await runCleanup(idempotencyKey);
      // The contract is explicit: a successful run can still report per-phase
      // problems, so the notice must not claim a clean sweep.
      notice.value = result.value.errors.length > 0 ? '清理已完成，但存在非致命错误（见下方列表）' : '清理已完成';
      return true;
    } catch (error) {
      failure.value = mapCleanupError(error);
      return false;
    } finally {
      running.value = false;
    }
  }

  async function restore(bundleId: string): Promise<boolean> {
    restoring.value = true;
    restoreFailure.value = null;
    restoreResult.value = null;
    notice.value = '';
    try {
      restoreResult.value = await unarchiveBundle(bundleId);
      notice.value = `已恢复 ${restoreResult.value.bundleId}（归档前状态 ${restoreResult.value.previousStatus}）`;
      return true;
    } catch (error) {
      restoreFailure.value = mapCleanupError(error);
      return false;
    } finally {
      restoring.value = false;
    }
  }

  return { running, failure, result, notice, restoring, restoreFailure, restoreResult, hasProblems, clean, restore };
});
