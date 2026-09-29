import { computed, ref } from 'vue';
import { defineStore } from 'pinia';
import {
  listStuckLocks,
  mapStuckLockError,
  mapStuckLockListError,
  releaseEmergencyLock,
  type ReleaseLockInput,
  type ReleaseLockResult,
  type StuckLockFailure,
  type StuckLockView,
} from '@/connect/stuck-lock-api';

/*
 * Stuck emergency locks (REQ-087, A10).
 *
 * Release is irreversible and not replayable, so the store re-reads the list after
 * every attempt: a successful release must disappear from the table, and a refusal
 * must be reported against the list the server actually holds.
 */
export const useStuckLocksStore = defineStore('stuckLocks', () => {
  const locks = ref<StuckLockView[]>([]);
  const definitionFilter = ref('');
  const loading = ref(false);
  const releasing = ref(false);
  const failure = ref<StuckLockFailure | null>(null);
  const notice = ref('');
  const lastRelease = ref<ReleaseLockResult | null>(null);

  const isEmpty = computed(() => !loading.value && !failure.value && locks.value.length === 0);

  async function fetchLocks(): Promise<void> {
    loading.value = true;
    try {
      locks.value = await listStuckLocks(definitionFilter.value.trim());
    } catch (error) {
      failure.value = mapStuckLockListError(error);
      locks.value = [];
    } finally {
      loading.value = false;
    }
  }

  async function load(filter = definitionFilter.value): Promise<void> {
    definitionFilter.value = filter;
    failure.value = null;
    await fetchLocks();
  }

  async function release(input: ReleaseLockInput): Promise<boolean> {
    releasing.value = true;
    failure.value = null;
    notice.value = '';
    lastRelease.value = null;
    try {
      const result = await releaseEmergencyLock(input);
      lastRelease.value = result;
      await fetchLocks();
      notice.value = `已释放锁 ${input.intentId}（effect ${result.effectStatus}）`;
      return true;
    } catch (error) {
      // Re-read first so the operator sees whether the lock is still there, then
      // report: a plain load() would wipe the message.
      const mapped = mapStuckLockError(error);
      await fetchLocks();
      failure.value = mapped;
      return false;
    } finally {
      releasing.value = false;
    }
  }

  return {
    locks,
    definitionFilter,
    loading,
    releasing,
    failure,
    notice,
    lastRelease,
    isEmpty,
    load,
    release,
  };
});
