<script setup lang="ts">
// REQUIRE_PROMOTION / REVERT convergence card (plan v3 Step 5): the two
// policies are mutually exclusive CTAs. REQUIRE_PROMOTION shows the single
// operation-atomic Convergence Task (or the Create-ValuesRevision entry);
// REVERT shows awaiting/reconciled status — never a Kubernetes rollback
// phrasing (AC-058-28/32/33).
import type { EmergencyResultDisplay } from '@/features/emergency/model';

defineProps<{
  result: EmergencyResultDisplay;
  /** True when effectStatus is APPLIED (authoritative evidence). */
  applied: boolean;
  canCreateValuesRevision: boolean;
}>();

const emit = defineEmits<{ 'open-convergence': [] }>();

function revertLabel(result: EmergencyResultDisplay): string {
  if (result.revertStatus === 'RECONCILED') {
    return `已对账（Operation ${result.reconciledByOperationId}）`;
  }
  if (result.revertStatus === 'AWAITING_STANDARD_RELEASE') {
    return '等待标准发布对账';
  }
  return '等待回退对账';
}
</script>

<template>
  <div class="convergence-card">
    <template v-if="result.convergencePolicy === 'REQUIRE_PROMOTION'">
      <h4>收敛任务（REQUIRE_PROMOTION）</h4>
      <ul v-if="result.convergenceTasks.length > 0" class="task-list">
        <li v-for="task in result.convergenceTasks" :key="task.taskId">
          <code>{{ task.taskId }}</code>
          <span class="status">{{ task.status }}</span>
        </li>
      </ul>
      <p v-else-if="applied" class="hint">结果已生效，等待创建收敛任务。</p>
      <p v-else class="hint">结果未确认生效前不会创建收敛任务。</p>
      <button
        v-if="applied && result.convergenceTasks.length === 0 && canCreateValuesRevision"
        type="button"
        class="primary"
        @click="emit('open-convergence')"
      >
        创建 ValuesRevision 收敛
      </button>
    </template>
    <template v-else>
      <h4>回退策略（REVERT_ON_NEXT_RECONCILE）</h4>
      <p class="status">{{ revertLabel(result) }}</p>
      <p class="hint">该策略不创建收敛任务，也不阻断标准发布（AC-058-32）。</p>
    </template>
  </div>
</template>

<style scoped>
.convergence-card { display: grid; gap: 0.6rem; padding: 0.9rem; border: 1px solid #e2e8f0; border-radius: 0.5rem; background: #fff; }
.task-list { display: grid; gap: 0.35rem; padding-left: 1.1rem; }
.status { color: #475569; font-size: 0.85rem; }
.hint { color: #64748b; font-size: 0.85rem; }
.primary { justify-self: start; padding: 0.5rem 1rem; border: 0; border-radius: 0.375rem; background: #2563eb; color: #fff; }
</style>
