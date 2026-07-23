<script setup lang="ts">
import { computed } from 'vue';
import { useRoute } from 'vue-router';

const route = useRoute();
const customerId = computed(() => typeof route.params.customerId === 'string' ? route.params.customerId : '');
const clusterRoutingEnabled = import.meta.env.VITE_FEATURE_CLUSTER_ROUTING !== 'false';
</script>

<template>
  <div class="app-shell">
    <header class="app-shell__header">
      <h1 class="app-shell__title">Release Manager</h1>
      <nav class="app-shell__nav">
        <RouterLink v-if="clusterRoutingEnabled && customerId" :to="{ name: 'ClusterList', params: { customerId } }">Clusters</RouterLink>
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
