import { createPinia, setActivePinia } from 'pinia';
import { describe, expect, it } from 'vitest';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import type { EmergencyAuthorizationProjection } from '@/connect/emergency-api';

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

function snapshot(overrides: Partial<EmergencyAuthorizationProjection> = {}): EmergencyAuthorizationProjection {
  return {
    organizationId: 'org1',
    customerId: 'cust1',
    bindingActive: true,
    customerActive: true,
    role: 'release_admin',
    canExecuteEmergency: true,
    canResolveEmergency: false,
    canCreateValuesRevision: true,
    canApproveValuesRevision: true,
    sourceVersion: 3n,
    policyVersion: 1n,
    checkpoint: 3n,
    fresh: true,
    actorId: 'user1',
    emergencyChangeEnabled: true,
    ...overrides,
  };
}

describe('emergencyAuthorization store (D1 fail-closed projection)', () => {
  it('loads the scoped snapshot and derives capabilities', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyAuthorizationStore();
    store.configure({ loadSnapshot: async () => snapshot() });

    await store.load('org1', 'cust1');
    expect(store.snapshot?.canExecuteEmergency).toBe(true);
    expect(store.featureEnabled).toBe(true);
    expect(store.canExecuteEmergency).toBe(true);
    expect(store.canCreateValuesRevision).toBe(true);
    expect(store.writeAllowed).toBe(true);
    expect(store.gateFor('execute')).toBe('allowed');
  });

  it('fails closed on missing capability → 403 and on kill switch → 404', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyAuthorizationStore();
    store.configure({ loadSnapshot: async () => snapshot({ canExecuteEmergency: false }) });
    await store.load('org1', 'cust1');
    expect(store.canExecuteEmergency).toBe(false);
    expect(store.gateFor('execute')).toBe('forbidden');

    store.configure({ loadSnapshot: async () => snapshot({ emergencyChangeEnabled: false }) });
    await store.load('org1', 'cust1');
    expect(store.featureEnabled).toBe(false);
    expect(store.gateFor('execute')).toBe('not_found');
    // Existing paths (convergence) stay reachable even with the kill switch on.
    expect(store.gateFor('createValues')).toBe('allowed');
  });

  it('maps scope/binding mismatch to 404', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyAuthorizationStore();
    store.configure({ loadSnapshot: async () => snapshot({ bindingActive: false }) });
    await store.load('org1', 'cust1');
    expect(store.gateFor('execute')).toBe('not_found');
  });

  it('fences stale scope responses: old result never writes a new scope', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyAuthorizationStore();
    const first = deferred<EmergencyAuthorizationProjection>();
    const second = deferred<EmergencyAuthorizationProjection>();
    let call = 0;
    store.configure({
      loadSnapshot: () => {
        call++;
        return call === 1 ? first.promise : second.promise;
      },
    });

    const loadingA = store.load('org1', 'cust1');
    const loadingB = store.load('org1', 'cust2');
    second.resolve(snapshot({ customerId: 'cust2' }));
    await loadingB;
    first.resolve(snapshot({ customerId: 'cust1' }));
    await loadingA;

    // The stale cust1 response must not overwrite the cust2 scope.
    expect(store.scopeKey).toBe('org1:cust2');
    expect(store.snapshot?.customerId).toBe('cust2');
  });

  it('fails closed before any snapshot is loaded', () => {
    setActivePinia(createPinia());
    const store = useEmergencyAuthorizationStore();
    expect(store.featureEnabled).toBe(false);
    expect(store.canExecuteEmergency).toBe(false);
    expect(store.writeAllowed).toBe(false);
  });
});
