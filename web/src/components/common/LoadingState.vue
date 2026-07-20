<script setup lang="ts">
interface LoadingStateProps {
  message?: string;
  label?: string;
}

withDefaults(defineProps<LoadingStateProps>(), {
  message: '',
  label: 'Loading',
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
  border: 3px solid var(--color-border, #e2e8f0);
  border-top-color: var(--color-primary, #2563eb);
  border-radius: 50%;
  animation: spin 0.6s linear infinite;
}

.loading-state__text {
  margin: 0;
  color: var(--color-muted, #64748b);
  font-size: 0.875rem;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
