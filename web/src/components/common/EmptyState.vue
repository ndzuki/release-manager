<script setup lang="ts">
defineProps<{ message?: string }>();
</script>

<template>
  <div class="empty-state">
    <div class="empty-state__icon">📭</div>
    <p class="empty-state__text">{{ message ?? 'No data' }}</p>
    <slot />
  </div>
</template>

<style scoped>
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 3rem 1rem;
  gap: 0.75rem;
}

.empty-state__icon {
  font-size: 2rem;
}

.empty-state__text {
  color: var(--color-muted, #64748b);
  font-size: 0.875rem;
}
</style>
