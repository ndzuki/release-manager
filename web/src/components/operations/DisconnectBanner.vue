<script setup lang="ts">
/*
 * REQ-057 item 11 requires an `aria-live` region but says nothing about politeness, so
 * it is decided here: a reconnect is a lingering state, not a new failure event (the
 * same call OperatorPollingNotice.vue:12 makes for its own `status`), and the timeline's
 * `historyGap` paragraph already owns the assertive announcement on this page. The
 * `role="status"` below IS the polite live region; stating `aria-live="polite"` makes
 * that explicit. The previous `assertive` contradicted the role (UX-045).
 */
defineProps<{ visible: boolean }>();
</script>

<template>
  <div v-if="visible" class="disconnect-banner" role="status" aria-live="polite">
    实时更新已断开，正在重连…
  </div>
</template>

<style scoped>
.disconnect-banner { padding: 0.7rem 1rem; border: 1px solid var(--color-warning-border); border-radius: 0.5rem; background: var(--color-warning-soft); color: var(--color-warning-ink); font-size: var(--font-size-md); }
</style>
