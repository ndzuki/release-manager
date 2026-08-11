<template>
  <div class="values-skeleton" aria-label="Loading values editor">
    <div class="values-skeleton__block values-skeleton__block--editor" />
    <div class="values-skeleton__block values-skeleton__block--diff" />
  </div>
</template>

<style scoped>
.values-skeleton { display: grid; grid-template-columns: minmax(0, 1.35fr) minmax(20rem, 0.65fr); gap: 1rem; }
.values-skeleton__block { min-height: 34rem; border-radius: 0.75rem; background: linear-gradient(90deg, #e2e8f0 25%, #f8fafc 50%, #e2e8f0 75%); background-size: 200% 100%; animation: pulse 1.25s infinite linear; }
.values-skeleton__block--diff { min-height: 30rem; }
@keyframes pulse { from { background-position: 200% 0; } to { background-position: -200% 0; } }
@media (max-width: 64rem) { .values-skeleton { grid-template-columns: 1fr; } }
</style>
