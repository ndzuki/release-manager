<script setup lang="ts">
import { computed } from 'vue'
import { RouterView, useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const navItems = [
  { path: '/', label: '概览', icon: '📊' },
  { path: '/customers', label: '客户管理', icon: '👥' },
  { path: '/charts', label: 'Chart 配置', icon: '📦' },
  { path: '/releases', label: '发布历史', icon: '🚀' },
  { path: '/certificates', label: '证书管理', icon: '🔒' },
]

const currentTitle = computed(() => navItems.find(n => route.path === n.path)?.label ?? '')

function navigate(path: string) { router.push(path) }
function handleLogout() { auth.logout(); router.push('/login') }
</script>

<template>
  <div class="app-shell">
    <aside class="sidebar">
      <div class="sidebar-brand">Release Manager</div>
      <nav>
        <button
          v-for="item in navItems"
          :key="item.path"
          :class="['nav-item', { active: route.path === item.path }]"
          @click="navigate(item.path)"
        >
          <span class="nav-icon">{{ item.icon }}</span>
          <span>{{ item.label }}</span>
        </button>
      </nav>
      <div class="sidebar-footer">
        <div class="user-info">{{ auth.user?.name ?? '未登录' }}</div>
        <button class="btn-outline" @click="handleLogout">退出</button>
      </div>
    </aside>
    <main class="main-content">
      <header class="page-header">
        <h1>{{ currentTitle }}</h1>
      </header>
      <RouterView />
    </main>
  </div>
</template>

<style scoped>
.app-shell { display: flex; min-height: 100vh; }

.sidebar {
  width: 240px;
  background: var(--color-surface);
  border-right: 1px solid var(--color-border);
  padding: 16px 0;
  display: flex;
  flex-direction: column;
}
.sidebar-brand {
  padding: 12px 20px;
  font-size: 18px;
  font-weight: 700;
  color: var(--color-primary);
  border-bottom: 1px solid var(--color-border);
  margin-bottom: 8px;
}

.nav-item {
  width: 100%;
  text-align: left;
  padding: 12px 20px;
  background: none;
  font-size: 15px;
  color: var(--color-text);
  border-radius: 0;
  display: flex;
  align-items: center;
  gap: 10px;
}
.nav-item:hover { background: #f0f2f5; }
.nav-item.active { background: #ecf5ff; color: var(--color-primary); font-weight: 600; }
.nav-icon { font-size: 18px; }

.sidebar-footer {
  margin-top: auto;
  padding: 16px 20px;
  border-top: 1px solid var(--color-border);
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.user-info { font-size: 13px; color: var(--color-text-secondary); }

.main-content { flex: 1; padding: 24px; overflow-y: auto; }
.page-header { margin-bottom: 24px; }
.page-header h1 { font-size: 24px; }
</style>
