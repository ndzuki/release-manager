<script setup lang="ts">
defineProps<{ visible: boolean }>();
</script>

<template>
  <div v-if="visible" class="disconnect-banner" role="status" aria-live="assertive">
    实时更新已断开，正在重连…
  </div>
</template>

<style scoped>
.disconnect-banner { padding: 0.7rem 1rem; border: 1px solid #fdba74; border-radius: 0.5rem; background: #fff7ed; color: #9a3412; font-size: 0.88rem; }
</style>
