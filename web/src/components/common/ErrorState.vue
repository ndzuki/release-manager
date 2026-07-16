<script setup lang="ts">
defineProps<{
  message?: string;
  details?: string;
}>();
</script>

<template>
  <div class="error-state" role="alert">
    <div class="error-state__icon">⚠️</div>
    <p class="error-state__text">{{ message ?? 'Something went wrong' }}</p>
    <p v-if="details" class="error-state__details">{{ details }}</p>
    <slot />
  </div>
</template>

<style scoped>
.error-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 3rem 1rem;
  gap: 0.75rem;
  text-align: center;
}

.error-state__icon {
  font-size: 2rem;
}

.error-state__text {
  color: var(--color-error, #dc2626);
  font-weight: 500;
}

.error-state__details {
  color: var(--color-muted, #64748b);
  font-size: 0.8125rem;
  max-width: 32rem;
  word-break: break-word;
}
</style>
