import { ref } from 'vue';
import { defineStore } from 'pinia';
import { t } from '@/i18n/messages';
import {
  createOrganization,
  disableOrganization,
  getOrganization,
  mapOrganizationError,
  type OrganizationFailure,
  type OrganizationView,
} from '@/connect/organization-api';

/*
 * Organization-level writes (REQ-026 / TASK-301).
 *
 * Kept apart from `useOrganizationStore` (membership) on purpose: the two surfaces
 * share nothing but the organization id, and one shared failure slot is exactly the
 * mistake TASK-303 documented — a member refusal would then surface inside the
 * organization's confirmation dialog, as if it were that operation's answer.
 *
 * The store owns the two contract points the UI cannot get right by itself:
 *  - DisableOrganization is a versioned write (a stale version answers ABORTED), so
 *    `read` exists to obtain the version the server holds NOW instead of trusting the
 *    login-time session snapshot;
 *  - CreateOrganization's membership write is best effort, so the success notice
 *    never claims the caller is an administrator of the new organization.
 */
export const useOrganizationsStore = defineStore('organizations', () => {
  const saving = ref(false);
  const failure = ref<OrganizationFailure | null>(null);
  const notice = ref('');

  /** Reads the current state of one organization (name, status, version). */
  async function read(orgId: string): Promise<OrganizationView | null> {
    failure.value = null;
    try {
      return await getOrganization(orgId);
    } catch (error) {
      failure.value = mapOrganizationError(error);
      return null;
    }
  }

  async function create(name: string): Promise<OrganizationView | null> {
    saving.value = true;
    failure.value = null;
    notice.value = '';
    try {
      const created = await createOrganization(name);
      notice.value = t('organization.create.success', { name: created.name });
      return created;
    } catch (error) {
      failure.value = mapOrganizationError(error);
      return null;
    } finally {
      saving.value = false;
    }
  }

  async function disable(orgId: string, expectedVersion: bigint): Promise<OrganizationView | null> {
    saving.value = true;
    failure.value = null;
    notice.value = '';
    try {
      const disabled = await disableOrganization(orgId, expectedVersion);
      notice.value = t('organization.disable.success', { name: disabled.name });
      return disabled;
    } catch (error) {
      failure.value = mapOrganizationError(error);
      return null;
    } finally {
      saving.value = false;
    }
  }

  return {
    saving,
    failure,
    notice,
    read,
    create,
    disable,
  };
});
