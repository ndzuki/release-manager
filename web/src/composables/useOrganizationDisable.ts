import { computed, ref, type Ref } from 'vue';
import { t } from '@/i18n/messages';
import type { OrganizationView } from '@/connect/organization-api';

/*
 * TASK-363: the disable-organization confirmation's state moved out of
 * OrganizationPage.
 *
 * Disabling is destructive and TERMINAL, so the RPC runs only from `confirm` — never
 * from the header button. Arming it re-reads the organization first: the version in
 * the session snapshot is from login time and DisableOrganization is a
 * compare-and-swap, so a stale value would be refused (ABORTED).
 *
 * Two failure slots on purpose. `failure` is rendered INSIDE the confirmation (the
 * page-level alert sits behind the fixed backdrop), while `armFailure` reports a
 * failure that happened BEFORE the dialog exists — the fresh read that supplies the
 * version, or the absence of an active organization. Sharing one slot is exactly the
 * TASK-303 defect: the dialog would then announce an error from another operation.
 *
 * A lost race means the row moved: the re-read supplies the version the server now
 * holds, and `confirm` adopts it so the hinted retry cannot fail forever.
 */

export interface OrganizationDisableOptions {
  /** The active organization id from the session snapshot; '' when there is none. */
  organizationId: () => string;
  /** The store's read, used to obtain the version the server holds NOW. */
  read: (orgId: string) => Promise<OrganizationView | null>;
  /** The store's versioned disable; null when the server refused it. */
  disable: (orgId: string, expectedVersion: bigint) => Promise<OrganizationView | null>;
  /** The mapped reason the store holds after a refusal. */
  failureMessage: () => string | null;
  /** The mapped failure code the store holds after a refusal. */
  failureCode: () => string | null;
}

export interface OrganizationDisable {
  pending: Ref<OrganizationView | null>;
  /** Rendered inside the still-open confirmation. */
  failure: Ref<string | null>;
  /** Rendered on the page, because no confirmation is open yet. */
  armFailure: Ref<string | null>;
  loading: Ref<boolean>;
  description: Ref<string>;
  arm: () => Promise<void>;
  close: () => void;
  confirm: () => Promise<boolean>;
}

export function useOrganizationDisable(options: OrganizationDisableOptions): OrganizationDisable {
  const pending = ref<OrganizationView | null>(null);
  const failure = ref<string | null>(null);
  const armFailure = ref<string | null>(null);
  const loading = ref(false);
  const description = computed(() =>
    pending.value ? t('organization.disable.description', { name: pending.value.name }) : '',
  );

  /** Reads the organization the server holds NOW before arming the confirmation. */
  async function arm(): Promise<void> {
    armFailure.value = null;
    failure.value = null;
    const orgId = options.organizationId();
    if (!orgId) {
      armFailure.value = t('organization.disable.unavailable');
      return;
    }
    loading.value = true;
    try {
      const fresh = await options.read(orgId);
      if (!fresh) {
        armFailure.value = options.failureMessage();
        return;
      }
      if (fresh.status === 'disabled') {
        armFailure.value = t('organization.disable.alreadyDisabled');
        return;
      }
      pending.value = fresh;
    } finally {
      loading.value = false;
    }
  }

  function close(): void {
    failure.value = null;
    pending.value = null;
  }

  async function confirm(): Promise<boolean> {
    const target = pending.value;
    if (!target) return false;
    failure.value = null;
    const disabled = await options.disable(target.id, target.optimisticVersion);
    if (disabled) {
      pending.value = null;
      return true;
    }
    failure.value = options.failureMessage();
    if (options.failureCode() === 'conflict') {
      // A lost race means the row moved: re-read so the hinted retry carries the
      // version the server now holds instead of the one that was just refused.
      const fresh = await options.read(target.id);
      if (fresh) pending.value = fresh;
      else failure.value = options.failureMessage() ?? failure.value;
    }
    return false;
  }

  return { pending, failure, armFailure, loading, description, arm, close, confirm };
}
