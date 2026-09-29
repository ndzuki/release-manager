import { ref } from 'vue';
import { defineStore } from 'pinia';
import {
  createBinding,
  listBindings,
  mapGovernanceError,
  revokeBinding,
  setCapabilityGrant,
  type AuthorizationAction,
  type BindingView,
  type GovernanceFailure,
  type GrantVersions,
} from '@/connect/binding-api';

/*
 * Bindings + capability grants state (REQ-049 / REQ-027, A2 + A3).
 *
 * Both writes are versioned, so the store reloads on ABORTED instead of leaving the
 * caller with a stale table (same discipline as the membership store).
 */
export const useGovernanceStore = defineStore('governance', () => {
  const orgId = ref('');
  const bindings = ref<BindingView[]>([]);
  const loading = ref(false);
  const saving = ref(false);
  const failure = ref<GovernanceFailure | null>(null);
  const notice = ref('');
  const grantVersions = ref<GrantVersions | null>(null);

  async function fetchBindings(): Promise<void> {
    loading.value = true;
    try {
      bindings.value = await listBindings(orgId.value);
    } catch (error) {
      failure.value = mapGovernanceError(error);
      bindings.value = [];
    } finally {
      loading.value = false;
    }
  }

  async function loadBindings(nextOrgId: string): Promise<void> {
    orgId.value = nextOrgId;
    failure.value = null;
    notice.value = '';
    await fetchBindings();
  }

  async function write(action: () => Promise<void>, successNotice: string): Promise<boolean> {
    saving.value = true;
    failure.value = null;
    notice.value = '';
    try {
      await action();
      await fetchBindings();
      notice.value = successNotice;
      return true;
    } catch (error) {
      const mapped = mapGovernanceError(error);
      // Refresh first when the server moved underneath us, then report: the fetch may
      // set its own failure and the message we want to show must win.
      if (mapped.code === 'conflict') await fetchBindings();
      failure.value = mapped;
      return false;
    } finally {
      saving.value = false;
    }
  }

  function bind(customerId: string): Promise<boolean> {
    return write(() => createBinding(orgId.value, customerId), `已绑定客户 ${customerId}`);
  }

  function revoke(binding: BindingView): Promise<boolean> {
    return write(
      () => revokeBinding(binding.id, binding.optimisticVersion),
      `已撤销与客户 ${binding.customerId} 的绑定`,
    );
  }

  /** There is no list-grants RPC; the server answers the resulting versions. */
  async function setCapability(
    subject: string,
    action: AuthorizationAction,
    revoked: boolean,
  ): Promise<boolean> {
    saving.value = true;
    failure.value = null;
    notice.value = '';
    grantVersions.value = null;
    try {
      grantVersions.value = await setCapabilityGrant(orgId.value, subject, action, revoked);
      notice.value = revoked
        ? `已撤销 ${subject} 的 ${action} 能力`
        : `已授予 ${subject} ${action} 能力`;
      return true;
    } catch (error) {
      failure.value = mapGovernanceError(error);
      return false;
    } finally {
      saving.value = false;
    }
  }

  return {
    orgId,
    bindings,
    loading,
    saving,
    failure,
    notice,
    grantVersions,
    loadBindings,
    bind,
    revoke,
    setCapability,
  };
});
