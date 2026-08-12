<script setup lang="ts">
import type { AuditExportTask } from '@/stores/audit';

const props = defineProps<{
  tasks: AuditExportTask[];
}>();
</script>

<template>
  <section v-if="props.tasks.length > 0" class="audit-exports" aria-label="Audit exports">
    <header>
      <h2>Exports</h2>
      <p>Export tasks are created asynchronously. The wire contract returns a receipt only;
        status polling is not available yet, and payloads are never cached in browser storage.</p>
    </header>
    <ul>
      <li v-for="task in props.tasks" :key="task.taskId" class="audit-exports__task">
        <div>
          <strong>{{ task.taskId }}</strong>
          <span class="audit-exports__status">{{ task.status }}</span>
        </div>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.audit-exports {
  display: grid;
  gap: 1rem;
  padding: 1rem;
  border: 1px solid #86efac;
  border-radius: 0.75rem;
  background: #f0fdf4;
}

.audit-exports header h2,
.audit-exports header p {
  margin: 0;
}
.audit-exports header p {
  color: #166534;
  font-size: 0.85rem;
}

.audit-exports ul {
  display: grid;
  gap: 0.75rem;
  margin: 0;
  padding: 0;
  list-style: none;
}

.audit-exports__task {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.75rem;
  border: 1px solid #bbf7d0;
  border-radius: 0.5rem;
  background: #fff;
}

.audit-exports__task strong {
  font-family: ui-monospace, monospace;
}

.audit-exports__status {
  margin-left: 0.5rem;
  padding: 0.15rem 0.45rem;
  border-radius: 999px;
  background: #dcfce7;
  color: #166534;
}
</style>

