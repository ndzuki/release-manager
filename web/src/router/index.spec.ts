import { createPinia, setActivePinia } from 'pinia';
import { createMemoryHistory } from 'vue-router';
import { Code, ConnectError } from '@connectrpc/connect';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createAppRouter, installAuthGuard } from './index';
import { setForbiddenNavigator, useAuthStore } from '@/stores/auth';

describe('auth route guard', () => {
  beforeEach(() => {
    setForbiddenNavigator(undefined);
    setActivePinia(createPinia());
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it('routes an uninitialized installation to first-time setup', async () => {
    const auth = useAuthStore();
    auth.$patch({ status: 'anonymous', initialized: false });
    const router = createAppRouter(createMemoryHistory());
    installAuthGuard(router);

    await router.push('/');
    await router.isReady();

    expect(router.currentRoute.value.name).toBe('Init');
  });

  it('preserves the protected return URL for anonymous sessions', async () => {
    const auth = useAuthStore();
    auth.$patch({ status: 'anonymous', initialized: true });
    const router = createAppRouter(createMemoryHistory());
    installAuthGuard(router);

    await router.push('/forbidden');
    await router.isReady();

    expect(router.currentRoute.value.name).toBe('Login');
    expect(auth.returnUrl).toBe('/forbidden');
  });

  it('marks expired sessions without bypassing server authorization', async () => {
    const auth = useAuthStore();
    auth.$patch({ status: 'expired', initialized: true });
    const router = createAppRouter(createMemoryHistory());
    installAuthGuard(router);

    await router.push('/');
    await router.isReady();

    expect(router.currentRoute.value.name).toBe('Login');
    expect(router.currentRoute.value.query.reason).toBe('expired');
  });

  it('navigates permission denial to 403 while retaining the session', async () => {
    const auth = useAuthStore();
    auth.$patch({
      status: 'authenticated',
      initialized: true,
      user: {
        $typeName: 'auth.v1.SessionUser',
        id: 'user-1',
        username: 'viewer',
        roles: ['viewer'],
        activeOrgId: 'org-1',
      },
    });
    const router = createAppRouter(createMemoryHistory());
    installAuthGuard(router);
    await router.push({ name: 'Home' });

    await auth.handleConnectError(new ConnectError('server denied the request', Code.PermissionDenied));

    expect(router.currentRoute.value.name).toBe('Forbidden');
    expect(auth.isAuthenticated).toBe(true);
    expect(auth.forbiddenMessage).toBe('server denied the request');
  });

  it('registers the operator routes with the static new path before the detail path', () => {
    const router = createAppRouter(createMemoryHistory());
    const paths = router.getRoutes().map((route) => route.path);

    expect(paths).toContain('/customers/:customerId/clusters/:clusterId/operators');
    expect(paths).toContain('/customers/:customerId/clusters/:clusterId/operators/new');
    expect(paths).toContain('/customers/:customerId/clusters/:clusterId/operators/:operatorId');
  });

  it('routes operator pages to not found when the feature is disabled', async () => {
    const auth = useAuthStore();
    auth.$patch({
      status: 'authenticated',
      initialized: true,
      user: {
        $typeName: 'auth.v1.SessionUser',
        id: 'user-1',
        username: 'admin',
        roles: ['release_admin'],
        activeOrgId: 'org-1',
      },
    });
    vi.stubEnv('VITE_FEATURE_OPERATOR_MANAGEMENT', 'false');
    const router = createAppRouter(createMemoryHistory());
    installAuthGuard(router);

    await router.push('/customers/customer-1/clusters/cluster-1/operators/new');
    await router.isReady();

    expect(router.currentRoute.value.name).toBe('NotFound');
  });
});
