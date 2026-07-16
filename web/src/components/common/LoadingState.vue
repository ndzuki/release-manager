<script setup lang="ts">
defineProps<{ message?: string }>();
</script>

<template>
  <div class="loading-state" role="status" aria-label="Loading">
    <div class="loading-state__spinner" />
    <p v-if="message" class="loading-state__text">{{ message }}</p>
  </div>
</template>

<style scoped>
.loading-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 3rem 1rem;
  gap: 1rem;
}

.loading-state__spinner {
  width: 2rem;
  height: 2rem;
  border: 3px solid var(--color-border, #e2e8f0);
  border-top-color: var(--color-primary, #3b82f6);
  border-radius: 50%;
  animation: spin 0.6s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.loading-state__text {
  color: var(--color-muted, #64748b);
  font-size: 0.875rem;
}
</style>
