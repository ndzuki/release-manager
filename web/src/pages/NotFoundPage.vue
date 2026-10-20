<script setup lang="ts">
import { t } from '@/i18n/messages';
import { useRouter } from 'vue-router';

const router = useRouter();
</script>

<template>
  <div class="not-found-page">
    <!-- The numeric code is decoration, not the heading: the page's h1 is what the reader
         came for ("页面不存在"), and the code is announced by the title anyway. Same
         pattern as ForbiddenState's 403 (TASK-394). -->
    <span class="not-found-page__code" aria-hidden="true">404</span>
    <h1 class="not-found-page__title">{{ t('notFound.titleText') }}</h1>
    <p class="not-found-page__body">{{ t('notFound.body') }}</p>
    <button @click="router.push({ name: 'Home' })">{{ t('notFound.home') }}</button>
  </div>
</template>

<style scoped>
.not-found-page {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  /* Shared error-page shell with ForbiddenPage: one 32rem column, centred both axes. */
  max-width: 32rem;
  margin: 48px auto;
  min-height: 60vh;
  gap: 0.75rem;
  text-align: center;
}

.not-found-page__code {
  color: var(--color-subtle);
  font-size: var(--font-size-3xl);
  font-weight: 700;
  line-height: var(--line-height-tight);
}

.not-found-page__title {
  margin: 0;
  color: var(--tx);
  font-size: var(--font-size-lg);
  font-weight: var(--font-weight-heading);
}

.not-found-page__body {
  margin: 0;
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
