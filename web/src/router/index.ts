import { createMemoryHistory, createRouter, createWebHistory } from 'vue-router';
import type { Router, RouterHistory, RouteLocationRaw } from 'vue-router';
import { setForbiddenNavigator, useAuthStore } from '@/stores/auth';

export function createAppRouter(
  history: RouterHistory = createWebHistory(),
  releaseInventoryEnabled = import.meta.env.VITE_ENABLE_RELEASE_INVENTORY !== 'false',
  valuesRevisionEnabled = import.meta.env.VITE_ENABLE_VALUES_REVISION !== 'false',
  operationsEnabled = import.meta.env.VITE_ENABLE_RELEASE_OPERATIONS !== 'false',
): Router {
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
      ...(releaseInventoryEnabled
        ? [{
            path: '/customers/:customerId/clusters/:clusterId/releases',
            name: 'ReleaseInventory',
            component: () => import('@/pages/ReleaseInventoryPage.vue'),
            meta: { requiresAuth: true },
          }]
        : []),
      ...(releaseInventoryEnabled && valuesRevisionEnabled
        ? [{
            path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/values',
            name: 'ValuesEditor',
            component: () => import('@/pages/ValuesEditorPage.vue'),
            meta: { requiresAuth: true },
          }]
        : []),
      ...(releaseInventoryEnabled && operationsEnabled
        ? [{
            path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/operations/new',
            name: 'OperationCreate',
            component: () => import('@/pages/OperationCreatePage.vue'),
            meta: { requiresAuth: true, requiresOperationCreate: true, feature: 'releaseOperations' },
          }, {
            path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/operations/:operationId',
            name: 'OperationDetail',
            component: () => import('@/pages/OperationDetailPage.vue'),
            meta: { requiresAuth: true, feature: 'releaseOperations' },
          }]
        : []),
      {
        path: '/',
        name: 'Home',
        component: () => import('@/pages/HomePage.vue'),
        meta: { requiresAuth: true },
      },
      {
        path: '/customers',
        name: 'CustomerList',
        component: () => import('@/pages/CustomerListPage.vue'),
        meta: { requiresAuth: true },
      },
      {
        path: '/customers/new',
        name: 'CustomerNew',
        component: () => import('@/pages/CustomerDetailPage.vue'),
        meta: { requiresAuth: true },
      },
      {
        path: '/customers/:id',
        name: 'CustomerDetail',
        component: () => import('@/pages/CustomerDetailPage.vue'),
        meta: { requiresAuth: true },
      },
      {
        path: '/customers/:customerId/clusters',
        name: 'ClusterList',
        component: () => import('@/pages/ClusterListPage.vue'),
        meta: { requiresAuth: true, feature: 'clusterRouting' },
      },
      {
        path: '/customers/:customerId/clusters/new',
        name: 'ClusterNew',
        component: () => import('@/pages/ClusterEditPage.vue'),
        meta: { requiresAuth: true, feature: 'clusterRouting', requiresWrite: true },
      },
      {
        path: '/customers/:customerId/clusters/:clusterId/operators/new',
        name: 'OperatorEnroll',
        component: () => import('@/pages/OperatorEnrollPage.vue'),
        meta: { requiresAuth: true, feature: 'operatorManagement' },
      },
      {
        path: '/customers/:customerId/clusters/:clusterId/operators/:operatorId',
        name: 'OperatorDetail',
        component: () => import('@/pages/OperatorDetailPage.vue'),
        meta: { requiresAuth: true, feature: 'operatorManagement' },
      },
      {
        path: '/customers/:customerId/clusters/:clusterId/operators',
        name: 'OperatorList',
        component: () => import('@/pages/OperatorListPage.vue'),
        meta: { requiresAuth: true, feature: 'operatorManagement' },
      },
      {
        path: '/customers/:customerId/clusters/:clusterId',
        name: 'ClusterDetail',
        component: () => import('@/pages/ClusterDetailPage.vue'),
        meta: { requiresAuth: true, feature: 'clusterRouting' },
      },
      {
        path: '/customers/:customerId/clusters/:clusterId/edit',
        name: 'ClusterEdit',
        component: () => import('@/pages/ClusterEditPage.vue'),
        meta: { requiresAuth: true, feature: 'clusterRouting', requiresWrite: true },
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
  setForbiddenNavigator(async () => {
    if (router.currentRoute.value.name !== 'Forbidden') {
      await router.push({ name: 'Forbidden' });
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

    if (to.meta.requiresWrite && !auth.canWrite) {
      return { name: 'ClusterList', params: { customerId: to.params.customerId } };
    }

    if (to.meta.feature === 'clusterRouting' && import.meta.env.VITE_FEATURE_CLUSTER_ROUTING === 'false') {
      return { name: 'NotFound' };
    }
    if (to.meta.feature === 'operatorManagement' && import.meta.env.VITE_FEATURE_OPERATOR_MANAGEMENT === 'false') {
      return { name: 'NotFound' };
    }
    if (to.meta.feature === 'releaseOperations' && import.meta.env.VITE_ENABLE_RELEASE_OPERATIONS === 'false') {
      return { name: 'NotFound' };
    }
    if (to.name === 'Login' && auth.isAuthenticated) {
      return { name: 'Home' };
    }
    if (to.meta.requiresAuth && !auth.isAuthenticated) {
      auth.setReturnUrl(to.fullPath);
      return { name: 'Login', query: auth.status === 'expired' ? { reason: 'expired' } : undefined };
    }
		if (to.meta.requiresOperationCreate && !auth.canCreateReleaseOperation) {
			return { name: 'Forbidden' };
		}

    return true;
  });
}

const router = createAppRouter(import.meta.env.MODE === 'test' ? createMemoryHistory() : createWebHistory());
installAuthGuard(router);

export default router;
