<script setup lang="ts">
import type { AuditExportTask } from '@/stores/audit';

const props = defineProps<{
  tasks: AuditExportTask[];
}>();

const emit = defineEmits<{
  refresh: [taskId: string];
}>();
</script>

<template>
  <section v-if="props.tasks.length > 0" class="audit-exports" aria-label="Audit exports">
    <header>
      <h2>Exports</h2>
      <p>Refresh tasks manually. Export payloads are not cached in browser storage.</p>
    </header>
    <ul>
      <li v-for="task in props.tasks" :key="task.taskId" class="audit-exports__task">
        <div>
          <strong>{{ task.taskId }}</strong>
          <span class="audit-exports__status">{{ task.status }}</span>
          <p v-if="task.errorMessage" role="alert">{{ task.errorMessage }}</p>
        </div>
        <div class="audit-exports__actions">
          <a v-if="task.status === 'ready' && task.downloadUrl" :href="task.downloadUrl">Download CSV</a>
          <button v-if="task.status !== 'ready'" type="button" @click="emit('refresh', task.taskId)">Refresh</button>
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
.audit-exports header p,
.audit-exports__task p {
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

.audit-exports__actions button,
.audit-exports__actions a {
  display: inline-block;
  padding: 0.45rem 0.7rem;
  border: 1px solid #16a34a;
  border-radius: 0.375rem;
  background: #fff;
  color: #166534;
  cursor: pointer;
  font: inherit;
  font-weight: 600;
  text-decoration: none;
}
</style>
