<script setup lang="ts">
/**
 * AppShell — the persistent layout wrapper for all authenticated pages.
 *
 * Placeholder for now; will be extended with navigation sidebar, top bar,
 * user avatar, and notification bell in subsequent atomic tasks.
 */
</script>

<template>
  <div class="app-shell">
    <header class="app-shell__header">
      <h1 class="app-shell__title">Release Manager</h1>
      <nav class="app-shell__nav">
        <slot name="nav" />
      </nav>
    </header>
    <main class="app-shell__main">
      <slot />
    </main>
  </div>
</template>

<style scoped>
.app-shell {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
}

.app-shell__header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 1.5rem;
  height: 3.5rem;
  border-bottom: 1px solid var(--color-border, #e2e8f0);
  background: var(--color-surface, #fff);
}

.app-shell__title {
  font-size: 1.125rem;
  font-weight: 600;
}

.app-shell__main {
  flex: 1;
  padding: 1.5rem;
}
</style>
