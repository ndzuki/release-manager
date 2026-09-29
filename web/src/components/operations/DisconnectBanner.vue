<script setup lang="ts">
defineProps<{ visible: boolean }>();
</script>

<template>
  <div v-if="visible" class="disconnect-banner" role="status" aria-live="assertive">
    实时更新已断开，正在重连…
  </div>
</template>

<style scoped>
.disconnect-banner { padding: 0.7rem 1rem; border: 1px solid var(--color-warning-border); border-radius: 0.5rem; background: var(--color-warning-soft); color: var(--color-warning-ink); font-size: var(--font-size-md); }
</style>
