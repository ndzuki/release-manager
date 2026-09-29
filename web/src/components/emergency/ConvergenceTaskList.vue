<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
// Pending convergence task list with server-projected selectability
// (plan v3 Step 7, AC-058-34/42): selectable rows carry checkboxes; bound or
// incompatible rows stay visible with their reason and a Continue link.
import type { ConvergenceTaskDisplay } from '@/features/emergency/model';

defineProps<{
  tasks: ConvergenceTaskDisplay[];
  selectedTaskIds: string[];
}>();

const emit = defineEmits<{ toggle: [taskId: string]; continue: [taskId: string] }>();

const opTypeLabel = (opType: string): string => statusLabel('emergencyOperation', opType);
</script>

<template>
  <table class="task-table">
    <thead>
      <tr>
        <th scope="col" aria-label="选择"></th>
        <th scope="col">{{ t('emergency.tasks.target') }}</th>
        <th scope="col">{{ t('emergency.tasks.type') }}</th>
        <th scope="col">{{ t('emergency.tasks.reason') }}</th>
        <th scope="col">{{ t('emergency.tasks.promotionPaths') }}</th>
        <th scope="col">{{ t('emergency.tasks.status') }}</th>
      </tr>
    </thead>
    <tbody>
      <tr v-for="task in tasks" :key="task.taskId">
        <td>
          <input
            v-if="task.selectable"
            type="checkbox"
            :checked="selectedTaskIds.includes(task.taskId)"
            :aria-label="`选择收敛任务 ${task.taskId}`"
            @change="emit('toggle', task.taskId)"
          />
          <span v-else aria-hidden="true">—</span>
        </td>
        <td>{{ task.targetSummary }}</td>
        <td>{{ opTypeLabel(task.opType) }}</td>
        <td class="reason">{{ task.reasonDisplay }}</td>
        <td><code v-for="path in task.promotionPaths" :key="path" class="path">{{ path }}</code></td>
        <td>
          <span v-if="task.activeRevisionId" class="status">
            {{ t('emergency.tasks.bound') }} {{ statusLabel('valuesRevision', task.activeRevisionStatus || 'draft') }}
            <button type="button" class="continue" @click="emit('continue', task.taskId)">{{ t('emergency.tasks.continue') }}</button>
          </span>
          <span v-else-if="!task.selectable" class="status muted">{{ task.incompatibilityReason || '不可选' }}</span>
          <span v-else class="status">{{ t('emergency.tasks.pendingPromotion') }}</span>
        </td>
      </tr>
    </tbody>
  </table>
</template>

<style scoped>
.task-table { width: 100%; border-collapse: collapse; }
.task-table th, .task-table td { padding: 0.6rem 0.75rem; border-bottom: 1px solid var(--color-border); text-align: left; }
.task-table th { color: var(--color-muted-strong); background: var(--color-bg); font-size: var(--font-size-xs); text-transform: uppercase; }
.reason { max-width: 16rem; overflow-wrap: anywhere; }
.path { display: block; font-size: var(--font-size-xs); color: var(--color-text-secondary); }
.status { font-size: var(--font-size-sm); color: var(--color-muted-strong); }
.muted { color: var(--color-subtle); }
.continue { margin-left: 0.5rem; color: var(--color-primary); }
</style>
