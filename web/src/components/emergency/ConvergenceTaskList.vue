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
  <div
    class="table-wrap task-table-wrap"
    role="region"
    :aria-label="t('emergency.tasks.scrollRegion')"
    tabindex="0"
  >
    <table class="task-table">
    <!-- UX-017 (W14): the header cells already carry scope; the missing piece
         was an accessible name for the table itself. -->
    <caption class="visually-hidden">{{ t('emergency.tasks.caption') }}</caption>
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
        <td class="type">{{ opTypeLabel(task.opType) }}</td>
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
  </div>
</template>

<style scoped>
/* Direction-C table frame (TASK-394). `.table-wrap` already owns horizontal containment,
   so the 6-column table scrolls inside its own box at 375px instead of widening the page;
   the frame is what stops the list floating on the page background. */
.task-table-wrap {
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
}
.task-table { width: 100%; min-width: 48rem; border-collapse: collapse; background: var(--sf); }
/* Quiet header band. It used to paint `--color-bg`, which is the page background rather
   than a header surface, so the header read as a hole rather than a band. */
.task-table th { padding: 10px 20px; background: var(--color-surface-muted); color: var(--color-muted-strong); font-size: var(--font-size-xs); font-weight: 600; text-align: left; text-transform: uppercase; white-space: nowrap; }
.task-table td { padding: 10px 20px; border-top: 1px solid var(--color-border-strong); text-align: left; }
/* A floor on the reason column: auto table layout hands its width to the long promotion
   path, which squeezed this cell to one glyph per line at 375px. */
.reason { min-width: 8rem; max-width: 16rem; overflow-wrap: anywhere; }
.path { display: block; font-size: var(--font-size-xs); color: var(--color-text-secondary); }
/* Short CJK label: without nowrap auto layout gives it one glyph per line at 375px. */
.type { white-space: nowrap; }
.status { font-size: var(--font-size-sm); color: var(--color-muted-strong); }
.muted { color: var(--color-subtle); }
.continue { margin-left: 0.5rem; color: var(--color-primary); }
</style>
