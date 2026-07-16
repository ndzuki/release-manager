import { createPinia, setActivePinia } from 'pinia';
import { createMemoryHistory } from 'vue-router';
import { beforeEach, describe, expect, it } from 'vitest';
import { createAppRouter, installAuthGuard } from './index';
import { setForbiddenNavigator, useAuthStore } from '@/stores/auth';

describe('auth route guard', () => {
  beforeEach(() => {
    setForbiddenNavigator(undefined);
    setActivePinia(createPinia());
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

  it('keeps an authenticated session on the forbidden page', async () => {
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
      forbiddenMessage: 'server denied the request',
    });
    const router = createAppRouter(createMemoryHistory());
    installAuthGuard(router);

    await router.push({ name: 'Forbidden' });
    await router.isReady();

    expect(router.currentRoute.value.name).toBe('Forbidden');
    expect(auth.isAuthenticated).toBe(true);
    expect(auth.forbiddenMessage).toBe('server denied the request');
  });
});
