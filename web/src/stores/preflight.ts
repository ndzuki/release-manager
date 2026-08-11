import { defineStore } from 'pinia';
import { computed, ref } from 'vue';
import { getOperation, mapOperationError } from '@/connect/operation-api';
import type { Operation } from '@/types/operation';

const pollingStates = new Set(['pending', 'preflight']);

export const usePreflightStore = defineStore('preflight', () => {
  const operationId = ref<string | null>(null);
  const operation = ref<Operation | null>(null);
  const polling = ref(false);
  const pollingIntervalMs = ref(3000);
  const error = ref<string | null>(null);
  let timer: ReturnType<typeof setTimeout> | undefined;
  let generation = 0;

  const shouldPoll = computed(() => operation.value !== null && pollingStates.has(operation.value.state));

  async function load(nextOperationId: string): Promise<void> {
    if (operationId.value !== nextOperationId) {
      stopPolling();
      operationId.value = nextOperationId;
      operation.value = null;
    }
    await refresh();
  }

  async function refresh(): Promise<void> {
    if (!operationId.value) return;
    const requestGeneration = ++generation;
    try {
      const latest = await getOperation(operationId.value);
      if (requestGeneration !== generation) return;
      operation.value = latest;
      error.value = null;
      if (pollingStates.has(latest.state)) startPolling();
      else stopPolling();
    } catch (requestError) {
      if (requestGeneration !== generation) return;
      error.value = mapOperationError(requestError).message;
      if (operation.value && pollingStates.has(operation.value.state)) startPolling();
    }
  }

  function startPolling(): void {
    if (!operationId.value || timer) return;
    polling.value = true;
    timer = setTimeout(async () => {
      timer = undefined;
      await refresh();
    }, pollingIntervalMs.value);
  }

  function stopPolling(): void {
    generation++;
    if (timer) clearTimeout(timer);
    timer = undefined;
    polling.value = false;
  }

  function reset(): void {
    stopPolling();
    operationId.value = null;
    operation.value = null;
    error.value = null;
  }

  return {
    operationId,
    operation,
    polling,
    pollingIntervalMs,
    error,
    shouldPoll,
    load,
    refresh,
    startPolling,
    stopPolling,
    reset,
  };
});
