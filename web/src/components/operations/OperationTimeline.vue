<script setup lang="ts">
import type { Operation } from '@/types/operation';

defineProps<{ operation: Operation }>();

const lifecycleStates = ['pending', 'preflight', 'queued', 'running', 'cancelling', 'succeeded'];
</script>

<template>
  <section class="operation-timeline" aria-labelledby="operation-timeline-title">
    <h2 id="operation-timeline-title">状态时间线</h2>
    <ol>
      <li
        v-for="state in lifecycleStates"
        :key="state"
        :class="{ 'operation-timeline__active': operation.state === state }"
      >
        <span aria-hidden="true" />
        {{ state }}
      </li>
      <li v-if="['failed', 'cancelled', 'timeout'].includes(operation.state)" class="operation-timeline__failure">
        <span aria-hidden="true" />
        {{ operation.state }}
      </li>
    </ol>
  </section>
</template>

<style scoped>
.operation-timeline { display: grid; gap: 0.75rem; }
.operation-timeline h2 { margin: 0; font-size: 1rem; }
.operation-timeline ol { display: flex; flex-wrap: wrap; gap: 0.5rem; padding: 0; margin: 0; list-style: none; }
.operation-timeline li { display: flex; align-items: center; gap: 0.4rem; padding: 0.45rem 0.65rem; border: 1px solid #cbd5e1; border-radius: 999px; color: #64748b; text-transform: uppercase; font-size: 0.75rem; }
.operation-timeline li span { width: 0.5rem; height: 0.5rem; border-radius: 50%; background: #94a3b8; }
.operation-timeline__active { border-color: #2563eb !important; color: #1d4ed8 !important; font-weight: 800; }
.operation-timeline__active span { background: #2563eb !important; }
.operation-timeline__failure { border-color: #ef4444 !important; color: #b91c1c !important; font-weight: 800; }
.operation-timeline__failure span { background: #dc2626 !important; }
</style>
