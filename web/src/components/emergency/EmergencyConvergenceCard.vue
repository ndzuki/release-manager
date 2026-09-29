<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
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
    return t('convergence.card.reconciled', { operationId: result.reconciledByOperationId ?? '' });
  }
  if (result.revertStatus === 'AWAITING_STANDARD_RELEASE') {
    return t('convergence.card.awaitingStandardRelease');
  }
  return t('convergence.card.awaitingRevert');
}
</script>

<template>
  <div class="convergence-card">
    <template v-if="result.convergencePolicy === 'REQUIRE_PROMOTION'">
      <h4>{{ t('convergence.card.title') }}</h4>
      <ul v-if="result.convergenceTasks.length > 0" class="task-list">
        <li v-for="task in result.convergenceTasks" :key="task.taskId">
          <code>{{ task.taskId }}</code>
          <span class="status">{{ statusLabel('convergence', task.status) }}</span>
        </li>
      </ul>
      <p v-else-if="applied" class="hint">{{ t('convergence.card.awaitingCreate') }}</p>
      <p v-else class="hint">{{ t('convergence.card.notConfirmed') }}</p>
      <button
        v-if="applied && result.convergenceTasks.length === 0 && canCreateValuesRevision"
        type="button"
        class="primary"
        @click="emit('open-convergence')"
      >
        {{ t('convergence.card.create') }}
      </button>
    </template>
    <template v-else>
      <h4>{{ t('convergence.card.revertPolicy') }}</h4>
      <p class="status">{{ revertLabel(result) }}</p>
      <p class="hint">{{ t('convergence.card.revertNote') }}</p>
    </template>
  </div>
</template>

<style scoped>
.convergence-card { display: grid; gap: 0.6rem; padding: 0.9rem; border: 1px solid var(--color-border); border-radius: 0.5rem; background: var(--color-surface); }
.task-list { display: grid; gap: 0.35rem; padding-left: 1.1rem; }
.status { color: var(--color-muted-strong); font-size: var(--font-size-sm); }
.hint { color: var(--color-muted); font-size: var(--font-size-sm); }
.primary { justify-self: start; padding: 0.5rem 1rem; border: 0; border-radius: 0.375rem; background: var(--color-primary); color: var(--color-on-accent); }
</style>
