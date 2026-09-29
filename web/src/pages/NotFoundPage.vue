<script setup lang="ts">
import { t } from '@/i18n/messages';
import { useRouter } from 'vue-router';

const router = useRouter();
</script>

<template>
  <div class="not-found-page">
    <h2>404</h2>
    <h3>{{ t('notFound.titleText') }}</h3>
    <p>{{ t('notFound.body') }}</p>
    <button @click="router.push({ name: 'Home' })">{{ t('notFound.home') }}</button>
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
  font-size: var(--font-size-3xl);
  font-weight: 700;
  color: var(--color-subtle);
}

.not-found-page p {
  color: var(--color-muted);
}

.not-found-page button {
  margin-top: 0.5rem;
  padding: 0.375rem 0.75rem;
  border: 1px solid var(--color-border-strong);
  border-radius: 0.375rem;
  background: var(--color-surface);
  cursor: pointer;
}
</style>
