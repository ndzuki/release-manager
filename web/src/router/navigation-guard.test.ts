import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it } from 'vitest';
import { createMemoryHistory } from 'vue-router';
import { createAppRouter, installAuthGuard } from './index';
import { useAuthStore } from '@/stores/auth';

/*
 * Plan N4: a read-only user who followed a "create cluster" link was silently sent
 * back to the list with no explanation, while the sibling guard for
 * `requiresOperationCreate` routed to the forbidden surface. Both must now explain
 * themselves.
 */
function routerWithGuard() {
  const router = createAppRouter(createMemoryHistory());
  installAuthGuard(router);
  return router;
}

function signIn(roles: string[]) {
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'dev-reader', roles, activeOrgId: 'org-1' },
    organizations: [{ $typeName: 'auth.v1.Organization', id: 'org-1', name: 'Platform', status: 'active' }],
  });
}

beforeEach(() => {
  setActivePinia(createPinia());
});

describe('auth guard write protection', () => {
  it('routes a read-only user to the forbidden surface with a reason', async () => {
    const router = routerWithGuard();
    signIn(['viewer']);

    await router.push({ name: 'ClusterNew', params: { customerId: 'c1' } });
    const auth = useAuthStore();

    expect(router.currentRoute.value.name).toBe('Forbidden');
    expect(auth.consumeForbiddenMessage()).toContain('写权限');
  });

  it('explains the refusal on the edit route as well', async () => {
    const router = routerWithGuard();
    signIn(['viewer']);

    await router.push({ name: 'ClusterEdit', params: { customerId: 'c1', clusterId: 'k1' } });

    expect(router.currentRoute.value.name).toBe('Forbidden');
    expect(useAuthStore().consumeForbiddenMessage()).toContain('写权限');
  });

  it('does not leave a stale refusal message when the role may write', async () => {
    const router = routerWithGuard();
    signIn(['release_admin']);

    await router.push({ name: 'ClusterNew', params: { customerId: 'c1' } });

    expect(useAuthStore().consumeForbiddenMessage()).toBeNull();
  });

  it('lets a writable role through', async () => {
    const router = routerWithGuard();
    signIn(['release_admin']);

    await router.push({ name: 'ClusterNew', params: { customerId: 'c1' } });

    expect(router.currentRoute.value.name).toBe('ClusterNew');
  });
});
