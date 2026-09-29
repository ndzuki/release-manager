<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
import type { AuditExportTask } from '@/stores/audit';

const props = defineProps<{
  tasks: AuditExportTask[];
}>();
</script>

<template>
  <section v-if="props.tasks.length > 0" class="audit-exports" :aria-label="t('audit.export.title')">
    <header>
      <h2>{{ t('audit.export.title') }}</h2>
      <p>{{ t('audit.export.note') }}</p>
    </header>
    <ul>
      <li v-for="task in props.tasks" :key="task.taskId" class="audit-exports__task">
        <div>
          <strong>{{ task.taskId }}</strong>
          <span class="audit-exports__status">{{ statusLabel('export', task.status) }}</span>
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
  border: 1px solid var(--color-success-border);
  border-radius: 0.75rem;
  background: var(--color-success-surface-soft);
}

.audit-exports header h2,
.audit-exports header p {
  margin: 0;
}
.audit-exports header p {
  color: var(--color-success-ink);
  font-size: var(--font-size-sm);
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
  border: 1px solid var(--color-success-border-soft);
  border-radius: 0.5rem;
  background: var(--color-surface);
}

.audit-exports__task strong {
  font-family: ui-monospace, monospace;
}

.audit-exports__status {
  margin-left: 0.5rem;
  padding: 0.15rem 0.45rem;
  border-radius: 999px;
  background: var(--color-success-surface);
  color: var(--color-success-ink);
}
</style>

