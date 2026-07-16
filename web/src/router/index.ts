import { createRouter, createWebHistory, type NavigationGuardNext, type RouteLocationNormalized } from 'vue-router';
import { useAuthStore } from '@/stores/auth';

// ---------------------------------------------------------------------------
// Route definitions
//
// Page components are lazy-loaded. New feature modules add their routes here.
// ---------------------------------------------------------------------------

const routes = [
  {
    path: '/login',
    name: 'Login',
    component: () => import('@/pages/LoginPage.vue'),
    meta: { requiresAuth: false },
  },
  {
    path: '/',
    name: 'Home',
    component: () => import('@/pages/HomePage.vue'),
    meta: { requiresAuth: true },
  },
  {
    // Catch-all: 404
    path: '/:pathMatch(.*)*',
    name: 'NotFound',
    component: () => import('@/pages/NotFoundPage.vue'),
    meta: { requiresAuth: false },
  },
];

const router = createRouter({
  history: createWebHistory(),
  routes,
});

// ---------------------------------------------------------------------------
// Global navigation guard
//
// 1. Pages with `requiresAuth: true` → redirect to /login if not authenticated.
// 2. `/login` when already authenticated → redirect to /.
// 3. `returnUrl` is preserved so we can redirect back after login.
// ---------------------------------------------------------------------------

router.beforeEach(
  (to: RouteLocationNormalized, _from: RouteLocationNormalized, next: NavigationGuardNext) => {
    const auth = useAuthStore();

    if (to.meta.requiresAuth !== false && !auth.isAuthenticated) {
      auth.setReturnUrl(to.fullPath);
      next({ name: 'Login' });
      return;
    }

    if (to.name === 'Login' && auth.isAuthenticated) {
      next({ name: 'Home' });
      return;
    }

    next();
  },
);

export default router;
