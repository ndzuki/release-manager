<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
// Emergency result panel (plan v3 Step 5, AC-058-20~33): renders the typed
// intent projection, optional before/after execution evidence, the orthogonal
// Operation state vs effectStatus, the Unresolved Emergency Effect warning,
// and the REQUIRE_PROMOTION/REVERT convergence card. All values are
// server-sanitized display projections — this panel never re-sanitizes or
// writes them anywhere else (AC-058-48).
import { computed } from 'vue';
import EmergencyConvergenceCard from '@/components/emergency/EmergencyConvergenceCard.vue';
import type { EffectObservationStatus } from '@/composables/useEmergencyEffectObservation';
import type { EmergencyResultDisplay, EmergencyTypedValuesDisplay } from '@/features/emergency/model';

const props = defineProps<{
  result: EmergencyResultDisplay | null;
  operationState: string;
  operationEffectStatus: string;
  observationStatus: EffectObservationStatus;
  canCreateValuesRevision: boolean;
}>();

const emit = defineEmits<{ 'open-convergence': [] }>();

const effectLabel: Record<string, string> = {
  NOT_STARTED: '尚未开始影响集群',
  UNKNOWN: '集群效果未知，继续观察并保留目标锁',
  APPLIED: '目标字段已写入集群',
  NOT_APPLIED: '未生效，不创建收敛任务',
};

const applied = computed(() => props.result?.effectStatus === 'APPLIED');

/** Terminal Operation with effectStatus UNKNOWN = Unresolved Emergency Effect. */
const unresolved = computed(
  () =>
    props.result?.effectStatus === 'UNKNOWN' &&
    ['succeeded', 'failed', 'cancelled', 'timeout'].includes(props.operationState),
);

function valuesLabel(values: EmergencyTypedValuesDisplay | null): string {
  if (!values) return '—';
  switch (values.case) {
    case 'image':
      return `image ${values.container} → ${values.imageReference}`;
    case 'replicas':
      return `replicas → ${values.replicas}`;
    case 'annotations':
      return `annotations ${values.annotations.map((entry) => `${entry.key}=${entry.value}`).join(', ')}`;
    default:
      return '—';
  }
}
</script>

<template>
  <section v-if="result" class="emergency-result" aria-label="紧急变更结果">
    <h3>{{ t('emergency.result.title') }}</h3>
    <dl class="result-grid">
      <div>
        <dt>{{ t('emergency.result.intent') }}</dt>
        <dd>{{ result.opType }} / {{ statusLabel('convergenceStrategy', result.convergencePolicy) }}</dd>
      </div>
      <div>
        <dt>受理状态</dt>
        <dd>{{ result.requested ? '已受理（执行异步进行）' : '未受理' }}</dd>
      </div>
      <div>
        <dt>{{ t('emergency.result.effectStatus') }}</dt>
        <dd :class="`effect-${result.effectStatus.toLowerCase()}`">
          {{ effectLabel[result.effectStatus] ?? result.effectStatus }}
        </dd>
      </div>
      <div>
        <dt>{{ t('emergency.result.before') }}</dt>
        <dd>{{ valuesLabel(result.before) }}</dd>
      </div>
      <div>
        <dt>{{ t('emergency.result.after') }}</dt>
        <dd>{{ valuesLabel(result.after) }}</dd>
      </div>
    </dl>

    <p v-if="unresolved" class="unresolved" role="alert">
      {{ t('emergency.result.unresolved') }}
    </p>
    <p v-if="observationStatus === 'resolved'" class="hint">{{ t('emergency.result.resolved') }}</p>

    <EmergencyConvergenceCard
      :result="result"
      :applied="applied"
      :can-create-values-revision="canCreateValuesRevision"
      @open-convergence="emit('open-convergence')"
    />
  </section>
</template>

<style scoped>
.emergency-result { display: grid; gap: 0.9rem; padding: 1rem; border: 1px solid var(--color-border); border-radius: 0.5rem; background: var(--color-surface); }
.result-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr)); gap: 0.6rem 1rem; }
.result-grid dt { color: var(--color-muted); font-size: var(--font-size-sm); }
.result-grid dd { margin: 0; }
.effect-applied { color: var(--color-success); }
.effect-unknown { color: var(--color-warning); }
.effect-not_applied, .effect-not_started { color: var(--color-muted); }
.unresolved { padding: 0.6rem 0.75rem; border: 1px solid var(--color-warning-border-strong); border-radius: 0.375rem; background: var(--color-warning-surface); color: var(--color-warning-ink-strong); }
.hint { color: var(--color-muted); font-size: var(--font-size-sm); }
</style>
