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

  it('registers and disables release feature routes', () => {
    const enabled = createAppRouter(createMemoryHistory(), true, true);
    const inventoryDisabled = createAppRouter(createMemoryHistory(), false, true);
    const valuesDisabled = createAppRouter(createMemoryHistory(), true, false);

    expect(enabled.resolve('/customers/customer-1/clusters/cluster-1/releases').name).toBe('ReleaseInventory');
    expect(enabled.resolve('/customers/customer-1/clusters/cluster-1/releases/definition-1/values').name).toBe('ValuesEditor');
    expect(inventoryDisabled.resolve('/customers/customer-1/clusters/cluster-1/releases').name).toBe('NotFound');
    expect(valuesDisabled.resolve('/customers/customer-1/clusters/cluster-1/releases/definition-1/values').name).toBe('NotFound');
  });

  it('registers and disables release operation routes independently', () => {
    const enabled = createAppRouter(createMemoryHistory(), true, true, true);
    const disabled = createAppRouter(createMemoryHistory(), true, true, false);

    expect(enabled.resolve('/customers/customer-1/clusters/cluster-1/releases/def-1/operations/new').name).toBe('OperationCreate');
    expect(enabled.resolve('/customers/customer-1/clusters/cluster-1/releases/def-1/operations/op-1').name).toBe('OperationDetail');
    expect(disabled.resolve('/customers/customer-1/clusters/cluster-1/releases/def-1/operations/new').name).toBe('NotFound');
  });

	it('allows only release administrators to enter the operation creation route', async () => {
		const deployerAuth = useAuthStore();
		deployerAuth.$patch({
			status: 'authenticated',
			initialized: true,
			user: {
				$typeName: 'auth.v1.SessionUser',
				id: 'user-deployer',
				username: 'deployer',
				roles: ['deployer'],
				activeOrgId: 'org-1',
			},
		});
		const deployerRouter = createAppRouter(createMemoryHistory());
		installAuthGuard(deployerRouter);
		await deployerRouter.push('/customers/customer-1/clusters/cluster-1/releases/def-1/operations/new');
		await deployerRouter.isReady();
		expect(deployerRouter.currentRoute.value.name).toBe('Forbidden');

		setActivePinia(createPinia());
		const adminAuth = useAuthStore();
		adminAuth.$patch({
			status: 'authenticated',
			initialized: true,
			user: {
				$typeName: 'auth.v1.SessionUser',
				id: 'user-release-admin',
				username: 'release-admin',
				roles: ['release_admin'],
				activeOrgId: 'org-1',
			},
		});
		const adminRouter = createAppRouter(createMemoryHistory());
		installAuthGuard(adminRouter);
		await adminRouter.push('/customers/customer-1/clusters/cluster-1/releases/def-1/operations/new');
		await adminRouter.isReady();
		expect(adminRouter.currentRoute.value.name).toBe('OperationCreate');
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
