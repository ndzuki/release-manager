import { createMemoryHistory, createRouter, createWebHistory } from 'vue-router';
import type { Router, RouterHistory, RouteLocationRaw } from 'vue-router';
import { setForbiddenNavigator, useAuthStore } from '@/stores/auth';

export function createAppRouter(history: RouterHistory = createWebHistory()): Router {
  return createRouter({
    history,
    routes: [
      {
        path: '/init',
        name: 'Init',
        component: () => import('@/pages/InitPage.vue'),
        meta: { public: true },
      },
      {
        path: '/login',
        name: 'Login',
        component: () => import('@/pages/LoginPage.vue'),
        meta: { public: true },
      },
      {
        path: '/forbidden',
        name: 'Forbidden',
        component: () => import('@/pages/ForbiddenPage.vue'),
        meta: { requiresAuth: true },
      },
      {
        path: '/',
        name: 'Home',
        component: () => import('@/pages/HomePage.vue'),
        meta: { requiresAuth: true },
      },
      {
        path: '/:pathMatch(.*)*',
        name: 'NotFound',
        component: () => import('@/pages/NotFoundPage.vue'),
        meta: { public: true },
      },
    ],
  });
}

export function installAuthGuard(router: Router): void {
  setForbiddenNavigator(() => {
    if (router.currentRoute.value.name !== 'Forbidden') {
      void router.push({ name: 'Forbidden' });
    }
  });
  router.beforeEach(async (to): Promise<RouteLocationRaw | boolean> => {
    const auth = useAuthStore();
    if (auth.status === 'idle') {
      await auth.initialize();
    }

    if (auth.initialized === false && to.name !== 'Init') {
      return { name: 'Init' };
    }
    if (auth.initialized === true && to.name === 'Init') {
      return auth.isAuthenticated ? { name: 'Home' } : { name: 'Login' };
    }
    if (to.name === 'Login' && auth.isAuthenticated) {
      return { name: 'Home' };
    }
    if (to.meta.requiresAuth && !auth.isAuthenticated) {
      auth.setReturnUrl(to.fullPath);
      return { name: 'Login', query: auth.status === 'expired' ? { reason: 'expired' } : undefined };
    }

    return true;
  });
}

const router = createAppRouter(import.meta.env.MODE === 'test' ? createMemoryHistory() : createWebHistory());
installAuthGuard(router);

export default router;
