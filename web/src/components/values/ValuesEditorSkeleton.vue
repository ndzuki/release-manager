<script setup lang="ts">
import { t } from '@/i18n/messages';
</script>

<template>
  <div class="values-skeleton" :aria-label="t('values.skeleton.label')">
    <div class="values-skeleton__block values-skeleton__block--editor" />
    <div class="values-skeleton__block values-skeleton__block--diff" />
  </div>
</template>

<style scoped>
.values-skeleton { display: grid; grid-template-columns: minmax(0, 1.35fr) minmax(20rem, 0.65fr); gap: 1rem; }
.values-skeleton__block { min-height: 34rem; border-radius: 0.75rem; background: linear-gradient(90deg, var(--color-border) 25%, var(--color-bg) 50%, var(--color-border) 75%); background-size: 200% 100%; animation: pulse 1.25s infinite linear; }
.values-skeleton__block--diff { min-height: 30rem; }
@keyframes pulse { from { background-position: 200% 0; } to { background-position: -200% 0; } }
@media (max-width: 64rem) { .values-skeleton { grid-template-columns: 1fr; } }
</style>
