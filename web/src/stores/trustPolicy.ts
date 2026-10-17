import { computed, ref } from 'vue';
import { defineStore } from 'pinia';
import {
  allowedActions,
  createTrustRoot,
  endGrace,
  getTrustPolicy,
  isLive,
  mapTrustError,
  retireTrustRoot,
  revokeTrustRoot,
  rotateTrustRoot,
  type RotateTrustRootInput,
  type TrustAction,
  type TrustFailure,
  type TrustPolicyView,
  type TrustRootInput,
  type TrustRootView,
} from '@/connect/trust-api';
import { t } from '@/i18n/messages';
import { useAuthStore } from '@/stores/auth';

/** Input the page owns; environment and operator come from the store's own state. */
export type TrustRootDraft = Omit<TrustRootInput, 'environment' | 'operator'>;
export type TrustRootRotationDraft = Omit<RotateTrustRootInput, 'environment' | 'operator'>;

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

  /**
   * Registers a new root (TASK-280 D11). Create and rotate share `apply`'s
   * reload-after-refusal rule: a write is ordered by the state the server holds, so
   * the operator must see the policy the refusal was about.
   */
  async function createRoot(draft: TrustRootDraft): Promise<boolean> {
    saving.value = true;
    failure.value = null;
    notice.value = '';
    try {
      const created = await createTrustRoot({ ...draft, environment: environment.value, operator: operator.value });
      await fetchPolicy();
      notice.value = t('trust.create.notice', { keyId: created.keyId });
      return true;
    } catch (error) {
      const mapped = mapTrustError(error);
      await fetchPolicy();
      failure.value = mapped;
      return false;
    } finally {
      saving.value = false;
    }
  }

  /**
   * Rotates an active root: the new public key is introduced and the old root moves
   * into its grace window. Only PUBLIC key material travels (see connect/trust-api.ts).
   */
  async function rotateRoot(draft: TrustRootRotationDraft): Promise<boolean> {
    saving.value = true;
    failure.value = null;
    notice.value = '';
    try {
      const rotated = await rotateTrustRoot({ ...draft, environment: environment.value, operator: operator.value });
      await fetchPolicy();
      notice.value = t('trust.rotate.notice', { keyId: rotated.newRoot.keyId });
      return true;
    } catch (error) {
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
    createRoot,
    rotateRoot,
  };
});
