<script setup lang="ts">
defineProps<{ message?: string }>();
</script>

<template>
  <div class="forbidden-state">
    <h2 class="forbidden-state__code">403</h2>
    <p class="forbidden-state__text">{{ message ?? 'You do not have permission to access this page.' }}</p>
    <slot />
  </div>
</template>

<style scoped>
.forbidden-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 3rem 1rem;
  gap: 0.75rem;
}

.forbidden-state__code {
  font-size: 3rem;
  font-weight: 700;
  color: var(--color-muted, #94a3b8);
}

.forbidden-state__text {
  color: var(--color-muted, #64748b);
}
</style>
