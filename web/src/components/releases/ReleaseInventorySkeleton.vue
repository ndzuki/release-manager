<template>
  <div class="skeleton-list" aria-label="正在加载 Release 列表" role="status">
    <div v-for="index in 5" :key="index" class="skeleton-row" aria-hidden="true">
      <span />
      <span />
      <span />
      <span />
    </div>
  </div>
</template>

<style scoped>
.skeleton-list { display: grid; gap: 0.65rem; }
.skeleton-row { display: grid; grid-template-columns: 1.4fr 0.8fr 1fr 0.8fr; gap: 1rem; padding: 1rem; border: 1px solid #e2e8f0; border-radius: 0.5rem; background: #fff; }
.skeleton-row span { height: 1rem; border-radius: 0.25rem; background: linear-gradient(90deg, #e2e8f0, #f8fafc, #e2e8f0); background-size: 200% 100%; animation: shimmer 1.2s infinite; }
@keyframes shimmer { to { background-position: -200% 0; } }
</style>
