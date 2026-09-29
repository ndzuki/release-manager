<script setup lang="ts">
import { t } from '@/i18n/messages';

interface LoadingStateProps {
  message?: string;
  label?: string;
}

withDefaults(defineProps<LoadingStateProps>(), {
  message: '',
  label: t('state.loading.label'),
});
</script>

<template>
  <section class="loading-state" role="status" :aria-label="label" aria-live="polite">
    <span class="loading-state__spinner" aria-hidden="true" />
    <p v-if="message" class="loading-state__text">{{ message }}</p>
  </section>
</template>

<style scoped>
.loading-state {
  display: grid;
  min-height: 12rem;
  place-items: center;
  align-content: center;
  gap: 1rem;
  padding: 3rem 1rem;
}

.loading-state__spinner {
  width: 2rem;
  height: 2rem;
  border: 3px solid var(--color-border);
  border-top-color: var(--color-primary);
  border-radius: 50%;
  animation: spin 0.6s linear infinite;
}

.loading-state__text {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--font-size-md);
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
