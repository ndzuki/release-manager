<script setup lang="ts">
import { useAuthStore } from '@/stores/auth';
import { useRouter } from 'vue-router';

const auth = useAuthStore();
const router = useRouter();

async function handleLogout() {
  await auth.logout();
  router.push({ name: 'Login' });
}
</script>

<template>
  <div class="home-page">
    <p>Welcome to Release Manager.</p>
    <p v-if="auth.user">Signed in as <strong>{{ auth.user.userId }}</strong>.</p>
    <button class="home-page__logout" @click="handleLogout">Sign out</button>
  </div>
</template>

<style scoped>
.home-page {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.home-page__logout {
  align-self: flex-start;
  padding: 0.375rem 0.75rem;
  border: 1px solid var(--color-border, #cbd5e1);
  border-radius: 0.375rem;
  background: var(--color-surface, #fff);
  cursor: pointer;
  font-size: 0.8125rem;
}
</style>
