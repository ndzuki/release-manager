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
            // W3 (UX plan N5): the operation history had no route, so a user who
            // left an operation detail could never get back to it.
            path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/operations',
            name: 'OperationList',
            component: () => import('@/pages/OperationListPage.vue'),
            meta: { requiresAuth: true, feature: 'releaseOperations' },
          }, {
            path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/operations/:operationId',
            name: 'OperationDetail',
            component: () => import('@/pages/OperationDetailPage.vue'),
            meta: { requiresAuth: true, feature: 'releaseOperations' },
          }, {
            // Emergency change entry (REQ-058 Step 6). The kill switch and
            // capability are server-authoritative (Authorization Snapshot);
            // the page resolves 403/404 — the guard only enforces auth here.
            path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/emergency',
            name: 'EmergencyChange',
            component: () => import('@/pages/EmergencyChangePage.vue'),
            meta: { requiresAuth: true, feature: 'releaseOperations' },
          }, {
            path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/emergency/convergence',
            name: 'ConvergenceTasks',
            component: () => import('@/pages/ConvergenceTasksPage.vue'),
            meta: { requiresAuth: true, feature: 'releaseOperations' },
          }]
        : []),
      {
        // A1 (UX plan §6.1): membership management had no console surface; the
        // four RPCs exist since REQ-026.
        // Local accounts are platform_admin-only server-side (procedure_policy.go);
        // the page renders ForbiddenState for anyone else.
        path: '/settings/users',
        name: 'LocalUsers',
        component: () => import('@/pages/LocalUsersPage.vue'),
        meta: { requiresAuth: true },
      },
      {
        path: '/settings/organization',
        name: 'OrganizationMembers',
        component: () => import('@/pages/OrganizationPage.vue'),
        meta: { requiresAuth: true },
      },
      {
        // A7 (UX plan §6.1): artifact lifecycle GC / bundle restore (REQ-069), which
        // only platform_admin may run (cleanup/write).
        path: '/artifacts/lifecycle',
        name: 'ArtifactLifecycle',
        component: () => import('@/pages/ArtifactLifecyclePage.vue'),
        meta: { requiresAuth: true, feature: 'releaseOperations' },
      },
      {
        // A8 (UX plan §6.1): bundles were only a hidden dropdown in the operation
        // form; their digests/evidence had no surface.
        path: '/bundles',
        name: 'Bundles',
        component: () => import('@/pages/BundlesPage.vue'),
        meta: { requiresAuth: true, feature: 'releaseOperations' },
      },
      {
        // A9 (UX plan §6.1): definitions had no list/editor; the promotion mapping
        // is the convergence prerequisite (REQ-040).
        path: '/definitions',
        name: 'Definitions',
        component: () => import('@/pages/DefinitionsPage.vue'),
        meta: { requiresAuth: true, feature: 'releaseOperations' },
      },
      {
        // A10 (UX plan §6.1): stuck emergency locks had no console surface
        // (REQ-087 ListStuckLocks/ReleaseEmergencyLock).
        path: '/emergency/stuck-locks',
        name: 'StuckLocks',
        component: () => import('@/pages/StuckLocksPage.vue'),
        meta: { requiresAuth: true, feature: 'releaseOperations' },
      },
      {
        // A5 (UX plan §6.1): trust roots had no console surface (REQ-012/REQ-043).
        path: '/settings/trust',
        name: 'TrustPolicy',
        component: () => import('@/pages/TrustPolicyPage.vue'),
        meta: { requiresAuth: true },
      },
      {
        // A2 + A3 (UX plan §6.1): org->customer bindings and capability grants had
        // no console surface; the RPCs exist since REQ-049 / REQ-027.
        path: '/settings/bindings',
        name: 'GovernanceBindings',
        component: () => import('@/pages/BindingsPage.vue'),
        meta: { requiresAuth: true },
      },
      {
        // A4 (UX plan §6.1): the console had no password-change surface at all,
        // although REQ-025 defines the RPC and revokes sessions on success.
        path: '/settings/password',
        name: 'ChangePassword',
        component: () => import('@/pages/ChangePasswordPage.vue'),
        meta: { requiresAuth: true },
      },
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
        path: '/audit',
        name: 'Audit',
        component: () => import('@/pages/AuditPage.vue'),
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
      // Plan N4: this used to redirect silently to the list, so a read-only user who
      // followed a "create cluster" link saw the list again with no explanation. The
      // sibling guard below already routes to the forbidden surface; say WHY here too.
      auth.setForbiddenMessage('需要写权限才能创建或修改集群；你的角色只有只读权限。');
      return { name: 'Forbidden' };
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
