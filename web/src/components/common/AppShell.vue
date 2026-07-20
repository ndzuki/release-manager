<script setup lang="ts">
import { useRouter } from 'vue-router';
import { useAuthStore } from '@/stores/auth';
import OrganizationSwitcher from './OrganizationSwitcher.vue';

const auth = useAuthStore();
const router = useRouter();

async function handleLogout(): Promise<void> {
  await auth.logout();
  await router.replace({ name: 'Login' });
}
</script>

<template>
  <div class="app-shell">
    <header class="app-shell__header">
      <RouterLink class="app-shell__brand" :to="{ name: 'Home' }">Release Manager</RouterLink>
      <nav class="app-shell__nav" aria-label="Primary navigation">
        <RouterLink :to="{ name: 'Home' }">Home</RouterLink>
      </nav>
      <div class="app-shell__session">
        <OrganizationSwitcher />
        <div class="app-shell__identity">
          <strong>{{ auth.user?.username }}</strong>
          <span>{{ auth.activeOrganization?.name }}</span>
        </div>
        <button class="app-shell__logout" type="button" @click="handleLogout">Sign out</button>
      </div>
    </header>
    <main class="app-shell__main">
      <slot />
    </main>
  </div>
</template>

<style scoped>
.app-shell {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  background: var(--color-bg, #f8fafc);
}

.app-shell__header {
  display: grid;
  grid-template-columns: auto 1fr auto;
  align-items: center;
  gap: 2rem;
  min-height: 4.5rem;
  padding: 0.75rem 1.5rem;
  border-bottom: 1px solid var(--color-border, #e2e8f0);
  background: var(--color-surface, #fff);
}

.app-shell__brand {
  color: #0f172a;
  font-size: 1.125rem;
  font-weight: 700;
  text-decoration: none;
}

.app-shell__nav {
  display: flex;
  gap: 1rem;
}

.app-shell__nav a {
  color: #334155;
  text-decoration: none;
}

.app-shell__session {
  display: flex;
  align-items: center;
  gap: 1rem;
}

.app-shell__identity {
  display: grid;
  min-width: 8rem;
  font-size: 0.8rem;
}

.app-shell__identity span {
  color: var(--color-muted, #64748b);
}

.app-shell__logout {
  padding: 0.45rem 0.75rem;
  border: 1px solid var(--color-border, #cbd5e1);
  border-radius: 0.375rem;
  background: var(--color-surface, #fff);
  cursor: pointer;
}

.app-shell__main {
  flex: 1;
  width: min(100%, 90rem);
  margin: 0 auto;
  padding: 2rem 1.5rem;
}

@media (max-width: 56rem) {
  .app-shell__header {
    grid-template-columns: 1fr;
    gap: 0.75rem;
  }

  .app-shell__session {
    flex-wrap: wrap;
  }
}
</style>
