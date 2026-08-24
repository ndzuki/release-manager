// Convergence task selection store (plan v3 Step 3 / Step 7 seam).
// Holds the paged pending tasks and selected IDs; compatibility is derived
// with the pure pre-check from features/emergency/validation.ts. The Prepare
// RPC stays the authoritative consistency boundary (AC-058-34).
//
// Contract divergence (recorded in the TASK record): ListConvergenceTasks has
// no cursor pagination in the canonical contract, so the list is loaded whole;
// selectable/incompatibility_reason come from the server projection.
import { defineStore } from 'pinia';
import { computed, ref } from 'vue';
import {
  createPrepareSession,
  listConvergenceTasks,
  type PrepareSessionDisplay,
} from '@/connect/emergency-api';
import { mapEmergencyError, type EmergencyErrorDisplay } from '@/features/emergency/errors';
import type { ConvergenceTaskDisplay } from '@/features/emergency/model';
import { validateConvergenceSelection } from '@/features/emergency/validation';

export interface ConvergenceSelectionOptions {
  /** Test seams: replace RPC loaders. */
  loadTasks?: (releaseDefinitionId: string, signal: AbortSignal) => Promise<ConvergenceTaskDisplay[]>;
  prepare?: (
    input: { releaseDefinitionId: string; taskIds: string[]; expectedParentVersion: bigint },
    signal: AbortSignal,
  ) => Promise<PrepareSessionDisplay>;
  abortController?: () => AbortController;
}

export const useConvergenceSelectionStore = defineStore('convergenceSelection', () => {
  const releaseDefinitionId = ref('');
  const tasks = ref<ConvergenceTaskDisplay[]>([]);
  const selectedTaskIds = ref<string[]>([]);
  const loading = ref(false);
  const loadError = ref<EmergencyErrorDisplay | null>(null);
  const preparing = ref(false);
  const prepareError = ref<EmergencyErrorDisplay | null>(null);
  const conflictTaskIds = ref<string[]>([]);
  const preparedSession = ref<PrepareSessionDisplay | null>(null);
  const options = ref<ConvergenceSelectionOptions>({});

  let generation = 0;
  let abortController: AbortController | null = null;

  const selectedTasks = computed(() =>
    tasks.value.filter((task) => selectedTaskIds.value.includes(task.taskId)),
  );

  const compatibility = computed(() => validateConvergenceSelection(selectedTasks.value));

  const canPrepare = computed(
    () =>
      selectedTaskIds.value.length > 0 &&
      compatibility.value.valid &&
      !preparing.value &&
      preparedSession.value === null,
  );

  function configure(next: ConvergenceSelectionOptions): void {
    options.value = { ...options.value, ...next };
  }

  async function load(nextDefinitionId: string, signal?: AbortSignal): Promise<void> {
    abortController?.abort();
    const controller = options.value.abortController ? options.value.abortController() : new AbortController();
    abortController = controller;
    const captured = ++generation;

    releaseDefinitionId.value = nextDefinitionId;
    tasks.value = [];
    selectedTaskIds.value = [];
    preparedSession.value = null;
    conflictTaskIds.value = [];
    prepareError.value = null;
    loadError.value = null;
    loading.value = true;

    const loadTasks = options.value.loadTasks ?? listConvergenceTasks;
    try {
      const response = await loadTasks(nextDefinitionId, signal ?? controller.signal);
      if (captured !== generation) return;
      // Server projections: non-selectable tasks stay visible with reasons.
      tasks.value = response;
    } catch (cause) {
      if (captured !== generation) return;
      if ((signal ?? controller.signal).aborted) return;
      loadError.value = mapEmergencyError(cause);
    } finally {
      if (captured === generation) loading.value = false;
    }
  }

  function toggleTask(taskId: string): void {
    const task = tasks.value.find((candidate) => candidate.taskId === taskId);
    if (!task) return;
    if (selectedTaskIds.value.includes(taskId)) {
      selectedTaskIds.value = selectedTaskIds.value.filter((id) => id !== taskId);
      return;
    }
    // Client pre-check: server-ineligible or bound tasks are not selectable.
    if (!task.selectable || task.activeRevisionId) return;
    selectedTaskIds.value = [...selectedTaskIds.value, taskId];
  }

  async function prepare(expectedParentVersion: bigint, signal?: AbortSignal): Promise<PrepareSessionDisplay | null> {
    if (!canPrepare.value) return null;
    preparing.value = true;
    prepareError.value = null;
    conflictTaskIds.value = [];
    const prepareFn = options.value.prepare ?? createPrepareSession;
    const captured = generation;
    try {
      const session = await prepareFn(
        {
          releaseDefinitionId: releaseDefinitionId.value,
          taskIds: [...selectedTaskIds.value],
          expectedParentVersion,
        },
        signal ?? abortController?.signal as AbortSignal,
      );
      if (captured !== generation) return null;
      preparedSession.value = session;
      if (session.conflictTaskIds.length > 0) {
        conflictTaskIds.value = [...session.conflictTaskIds];
        prepareError.value = {
          code: 'convergence_conflict',
          message: `任务冲突：${session.conflictTaskIds.join(', ')}`,
          retryable: false,
          typed: false,
        };
        return null;
      }
      return session;
    } catch (cause) {
      if (captured !== generation) return null;
      if ((signal ?? abortController?.signal)?.aborted) return null;
      prepareError.value = mapEmergencyError(cause);
      return null;
    } finally {
      if (captured === generation) preparing.value = false;
    }
  }

  function clearPrepared(): void {
    preparedSession.value = null;
    conflictTaskIds.value = [];
    prepareError.value = null;
  }

  function reset(): void {
    generation++;
    abortController?.abort();
    abortController = null;
    releaseDefinitionId.value = '';
    tasks.value = [];
    selectedTaskIds.value = [];
    loading.value = false;
    loadError.value = null;
    preparing.value = false;
    prepareError.value = null;
    conflictTaskIds.value = [];
    preparedSession.value = null;
  }

  return {
    releaseDefinitionId,
    tasks,
    selectedTaskIds,
    loading,
    loadError,
    preparing,
    prepareError,
    conflictTaskIds,
    preparedSession,
    selectedTasks,
    compatibility,
    canPrepare,
    configure,
    load,
    toggleTask,
    prepare,
    clearPrepared,
    reset,
  };
});
