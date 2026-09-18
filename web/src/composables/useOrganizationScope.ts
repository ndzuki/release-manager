import { onScopeDispose } from 'vue';
import { organizationChangedEvent } from '@/stores/auth';

/**
 * Organization-scoped data the shell has to drop when the active organization
 * changes. Passed in rather than imported so the reset is testable without a
 * Pinia instance, mirroring useEmergencyEffectObservation.
 */
export interface OrganizationScopeOptions {
  /** Drops the cached customer list, selection, history and draft. */
  resetCustomers: () => void;
  /** Reloads the customer list under the new organization scope. */
  reloadCustomers: () => Promise<void>;
  /** Drops the cached authorization snapshot and its scope key. */
  resetAuthorization: () => void;
  /** Reloads the bootstrap snapshot for the new scope. */
  reloadAuthorization: (organizationId: string, customerId: string) => Promise<void>;
  /** Customer currently in the route, if any. */
  customerId: () => string;
}

/**
 * Keeps the shell's organization-scoped caches in step with the active
 * organization (REQ-033 D-72): when SwitchOrganization completes, the customer
 * selection is relinked, the bootstrap snapshot is reloaded and every
 * organization-domain cache is dropped.
 *
 * The caches are cleared *before* anything is reloaded, so a page is never
 * driven by the previous organization's scope — the intermediate state D-72
 * forbids. Reloads are best-effort: a failed reload leaves the caches empty and
 * the page shows its own error state rather than stale data from the old scope.
 */
export function useOrganizationScope(options: OrganizationScopeOptions): void {
  async function apply(organizationId: string): Promise<void> {
    options.resetCustomers();
    options.resetAuthorization();
    try {
      await options.reloadCustomers();
      const customerId = options.customerId();
      if (customerId) await options.reloadAuthorization(organizationId, customerId);
    } catch {
      // The reload paths surface their own error state; swallowing here keeps a
      // failed refresh from rejecting the event listener.
    }
  }

  function onOrganizationChanged(event: Event): void {
    const detail = (event as CustomEvent<{ organizationId?: unknown }>).detail;
    const organizationId = typeof detail?.organizationId === 'string' ? detail.organizationId : '';
    if (!organizationId) return;
    void apply(organizationId);
  }

  globalThis.addEventListener?.(organizationChangedEvent, onOrganizationChanged);
  onScopeDispose(() => globalThis.removeEventListener?.(organizationChangedEvent, onOrganizationChanged));
}
