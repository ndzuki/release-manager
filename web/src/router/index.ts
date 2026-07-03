import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const routes = [
  {
    path: '/login',
    name: 'Login',
    component: () => import('@/views/LoginView.vue'),
    meta: { guest: true },
  },
  {
    path: '/',
    component: () => import('@/components/AppLayout.vue'),
    meta: { requiresAuth: true },
    children: [
      { path: '', name: 'Dashboard', component: () => import('@/views/DashboardView.vue') },
      { path: 'customers', name: 'Customers', component: () => import('@/views/CustomersView.vue') },
      { path: 'charts', name: 'Charts', component: () => import('@/views/ChartsView.vue') },
      { path: 'releases', name: 'Releases', component: () => import('@/views/ReleasesView.vue') },
      { path: 'certificates', name: 'Certificates', component: () => import('@/views/CertificatesView.vue') },
    ],
  },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

router.beforeEach((to, _from) => {
  const auth = useAuthStore()
  if (to.meta.requiresAuth && !auth.isLoggedIn) {
    return '/login'
  }
  if (to.meta.guest && auth.isLoggedIn) {
    return '/'
  }
})

export default router
