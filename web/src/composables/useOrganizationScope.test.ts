import { describe, expect, it, vi } from 'vitest';
import { effectScope, nextTick } from 'vue';
import { useOrganizationScope } from '@/composables/useOrganizationScope';
import { organizationChangedEvent } from '@/stores/auth';

function fakeScope(customerId = '') {
  const calls: string[] = [];
  return {
    calls,
    options: {
      resetCustomers: vi.fn(() => calls.push('resetCustomers')),
      reloadCustomers: vi.fn(async () => {
        calls.push('reloadCustomers');
      }),
      resetAuthorization: vi.fn(() => calls.push('resetAuthorization')),
      reloadAuthorization: vi.fn(async (organizationId: string, id: string) => {
        calls.push(`reloadAuthorization:${organizationId}:${id}`);
      }),
      customerId: () => customerId,
    },
  };
}

function dispatchOrganizationChanged(organizationId: unknown): void {
  globalThis.dispatchEvent(new CustomEvent(organizationChangedEvent, { detail: { organizationId } }));
}

describe('useOrganizationScope (REQ-033 D-72)', () => {
  it('drops both caches before reloading, so no page keeps the old scope', async () => {
    const scope = fakeScope('cust-1');
    const app = effectScope();
    app.run(() => useOrganizationScope(scope.options));

    dispatchOrganizationChanged('org-2');
    await nextTick();

    // The reset must precede the reloads: a page rendering between the two would
    // otherwise still be driven by the previous organization's data.
    expect(scope.calls.slice(0, 2)).toEqual(['resetCustomers', 'resetAuthorization']);
    expect(scope.options.reloadCustomers).toHaveBeenCalledTimes(1);
    expect(scope.options.reloadAuthorization).toHaveBeenCalledWith('org-2', 'cust-1');
    app.stop();
  });

  it('reloads the snapshot only when a customer is in scope', async () => {
    const scope = fakeScope('');
    const app = effectScope();
    app.run(() => useOrganizationScope(scope.options));

    dispatchOrganizationChanged('org-2');
    await nextTick();

    expect(scope.options.reloadCustomers).toHaveBeenCalledTimes(1);
    expect(scope.options.reloadAuthorization).not.toHaveBeenCalled();
    app.stop();
  });

  it('ignores an event without a usable organization id', async () => {
    const scope = fakeScope('cust-1');
    const app = effectScope();
    app.run(() => useOrganizationScope(scope.options));

    dispatchOrganizationChanged(undefined);
    dispatchOrganizationChanged('');
    await nextTick();

    expect(scope.options.resetCustomers).not.toHaveBeenCalled();
    expect(scope.options.resetAuthorization).not.toHaveBeenCalled();
    app.stop();
  });

  it('keeps a failed reload from rejecting the listener and leaves caches empty', async () => {
    const scope = fakeScope('cust-1');
    scope.options.reloadCustomers.mockRejectedValueOnce(new Error('offline'));
    const app = effectScope();
    app.run(() => useOrganizationScope(scope.options));

    dispatchOrganizationChanged('org-2');
    await nextTick();
    await nextTick();

    // Both caches were dropped, so the page falls back to its own error state
    // instead of rendering the previous organization's data.
    expect(scope.options.resetCustomers).toHaveBeenCalledTimes(1);
    expect(scope.options.resetAuthorization).toHaveBeenCalledTimes(1);
    expect(scope.options.reloadAuthorization).not.toHaveBeenCalled();
    app.stop();
  });

  it('stops listening once the scope is disposed', async () => {
    const scope = fakeScope('cust-1');
    const app = effectScope();
    app.run(() => useOrganizationScope(scope.options));
    app.stop();

    dispatchOrganizationChanged('org-2');
    await nextTick();

    expect(scope.options.resetCustomers).not.toHaveBeenCalled();
  });
});
