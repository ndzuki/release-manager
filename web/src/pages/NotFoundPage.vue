<script setup lang="ts">
import { useRouter } from 'vue-router';

const router = useRouter();
</script>

<template>
  <div class="not-found-page">
    <h2>404</h2>
    <p>The page you requested does not exist.</p>
    <button @click="router.push({ name: 'Home' })">Go home</button>
  </div>
</template>

<style scoped>
.not-found-page {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  min-height: 60vh;
  gap: 0.75rem;
  text-align: center;
}

.not-found-page h2 {
  font-size: 3rem;
  font-weight: 700;
  color: var(--color-muted, #94a3b8);
}

.not-found-page p {
  color: var(--color-muted, #64748b);
}

.not-found-page button {
  margin-top: 0.5rem;
  padding: 0.375rem 0.75rem;
  border: 1px solid var(--color-border, #cbd5e1);
  border-radius: 0.375rem;
  background: var(--color-surface, #fff);
  cursor: pointer;
}
</style>
