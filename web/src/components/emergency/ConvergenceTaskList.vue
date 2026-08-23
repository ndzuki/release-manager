<script setup lang="ts">
// Pending convergence task list with server-projected selectability
// (plan v3 Step 7, AC-058-34/42): selectable rows carry checkboxes; bound or
// incompatible rows stay visible with their reason and a Continue link.
import type { ConvergenceTaskDisplay } from '@/features/emergency/model';

defineProps<{
  tasks: ConvergenceTaskDisplay[];
  selectedTaskIds: string[];
}>();

const emit = defineEmits<{ toggle: [taskId: string]; continue: [taskId: string] }>();

function opTypeLabel(opType: string): string {
  switch (opType) {
    case 'SET_CONTAINER_IMAGE':
      return '镜像变更';
    case 'SET_REPLICAS':
      return '副本变更';
    case 'SET_APPROVED_ANNOTATION':
      return '注解变更';
    default:
      return opType;
  }
}
</script>

<template>
  <table class="task-table">
    <thead>
      <tr>
        <th scope="col" aria-label="选择"></th>
        <th scope="col">目标</th>
        <th scope="col">类型</th>
        <th scope="col">原因</th>
        <th scope="col">Promotion paths</th>
        <th scope="col">状态</th>
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
            已绑定 {{ task.activeRevisionStatus || 'draft' }}
            <button type="button" class="continue" @click="emit('continue', task.taskId)">Continue</button>
          </span>
          <span v-else-if="!task.selectable" class="status muted">{{ task.incompatibilityReason || '不可选' }}</span>
          <span v-else class="status">pending_promotion</span>
        </td>
      </tr>
    </tbody>
  </table>
</template>

<style scoped>
.task-table { width: 100%; border-collapse: collapse; }
.task-table th, .task-table td { padding: 0.6rem 0.75rem; border-bottom: 1px solid #e2e8f0; text-align: left; }
.task-table th { color: #475569; background: #f8fafc; font-size: 0.75rem; text-transform: uppercase; }
.reason { max-width: 16rem; overflow-wrap: anywhere; }
.path { display: block; font-size: 0.75rem; color: #334155; }
.status { font-size: 0.85rem; color: #475569; }
.muted { color: #94a3b8; }
.continue { margin-left: 0.5rem; color: #2563eb; }
</style>
