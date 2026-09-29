import { computed, ref } from 'vue';
import { defineStore } from 'pinia';
import {
  allowedActions,
  endGrace,
  getTrustPolicy,
  isLive,
  mapTrustError,
  retireTrustRoot,
  revokeTrustRoot,
  type TrustAction,
  type TrustFailure,
  type TrustPolicyView,
  type TrustRootView,
} from '@/connect/trust-api';
import { useAuthStore } from '@/stores/auth';

/*
 * Trust root policy state (REQ-012 / REQ-043, A5).
 *
 * Writes are state-ordered rather than version-checked, so the store reloads after
 * every action and after every refusal: the operator must see the state the server
 * actually holds before choosing the next step.
 */
export const useTrustPolicyStore = defineStore('trustPolicy', () => {
  const auth = useAuthStore();

  const environment = ref<string>('staging');
  const policy = ref<TrustPolicyView | null>(null);
  const loading = ref(false);
  const saving = ref(false);
  const failure = ref<TrustFailure | null>(null);
  const notice = ref('');

  const liveRootCount = computed(() => policy.value?.roots.filter((root) => isLive(root.state)).length ?? 0);
  const operator = computed(() => auth.user?.username ?? '');

  /** An environment with no roots is valid ("nothing trusted yet"), not an error. */
  const isEmpty = computed(() => !loading.value && !failure.value && (policy.value?.roots.length ?? 0) === 0);

  /**
   * Mirrors the two server rules: the action must be legal for the state it last
   * read, and it must not remove the last live root of the environment.
   */
  function canApply(root: TrustRootView, action: TrustAction): boolean {
    if (!allowedActions(root.state).includes(action)) return false;
    if (action === 'end_grace') return root.state === 'grace';
    if (liveRootCount.value <= 1 && isLive(root.state)) return false;
    return true;
  }

  /** Fetches the snapshot without clearing a failure that is being reported. */
  async function fetchPolicy(): Promise<void> {
    loading.value = true;
    try {
      policy.value = await getTrustPolicy(environment.value);
    } catch (error) {
      failure.value = mapTrustError(error);
      policy.value = null;
    } finally {
      loading.value = false;
    }
  }

  async function load(nextEnvironment = environment.value): Promise<void> {
    environment.value = nextEnvironment;
    failure.value = null;
    await fetchPolicy();
  }

  async function apply(root: TrustRootView, action: TrustAction): Promise<boolean> {
    saving.value = true;
    failure.value = null;
    notice.value = '';
    try {
      if (action === 'end_grace') await endGrace(environment.value, root.id, operator.value);
      else if (action === 'retire') await retireTrustRoot(environment.value, root.id, operator.value);
      else await revokeTrustRoot(environment.value, root.id, operator.value);
      await fetchPolicy();
      notice.value = `已对信任根 ${root.keyId} 执行 ${action}`;
      return true;
    } catch (error) {
      // The refusal is about the state the server holds; re-read first so the operator
      // sees the real state, then report (a plain load() would wipe the message).
      const mapped = mapTrustError(error);
      await fetchPolicy();
      failure.value = mapped;
      return false;
    } finally {
      saving.value = false;
    }
  }

  return {
    environment,
    policy,
    loading,
    saving,
    failure,
    notice,
    liveRootCount,
    operator,
    isEmpty,
    canApply,
    load,
    apply,
  };
});
