<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
import { computed } from 'vue';
import type { PreflightResult } from '@/types/operation';

// Preflight stage results (TASK-149 / REQ-056 AC-056-03): highlight the stage
// that failed and expand its checks. The panel renders nothing while preflight
// is still in flight, so the caller can mount it unconditionally.
const props = defineProps<{ result: PreflightResult | null }>();

/**
 * UX-002: `overall` is typed `'passed' | 'failed' | string`, so the previous
 * `overall === 'failed' ? '失败' : '通过'` labelled every other value — pending,
 * skipped, cancelled, empty — as **通过**: a failure (or an unfinished check)
 * rendered as a pass.
 *
 * Server vocabulary (internal/orchestrator/preflight/coordinator.go):
 *   failed / timeout → failure (a timeout is recorded as cancelled)
 *   cancelled        → the operation was cancelled
 *   skipped          → explicitly "not a pass and not a failure"
 *   anything else    → passed
 *
 * Evidence wins over the single string: a stage reporting failed/timeout while
 * `overall` says passed is still treated as a failure.
 */
const FAILING_STAGE_STATUSES = new Set(['failed', 'timeout']);
const CANCELLING_STAGE_STATUSES = new Set(['cancelled']);

type OverallTone = 'passed' | 'failed' | 'cancelled' | 'unknown';

const overallView = computed<{ tone: OverallTone; label: string }>(() => {
  const raw = (props.result?.overall ?? '').trim().toLowerCase();
  const statuses = (props.result?.stages ?? []).map((stage) => (stage.status ?? '').trim().toLowerCase());

  // failedStage is evidence too: a server that names a failed stage while the
  // overall string says passed must not be rendered as a pass.
  if (raw === 'failed' || Boolean(props.result?.failedStage) || statuses.some((status) => FAILING_STAGE_STATUSES.has(status))) {
    return { tone: 'failed', label: '失败' };
  }
  if (raw === 'cancelled' || statuses.some((status) => CANCELLING_STAGE_STATUSES.has(status))) {
    return { tone: 'cancelled', label: '已取消' };
  }
  if (raw === 'passed') {
    return { tone: 'passed', label: '通过' };
  }
  // Unknown values must not read as a pass: show what the server actually said.
  return {
    tone: 'unknown',
    label: t('operation.preflight.unknown', { reason: raw || t('operation.preflight.notReturned') }),
  };
});

const failedStages = computed(() => {
  const stages = props.result?.stages ?? [];
  return new Set([
    ...(props.result?.failedStage ? [props.result.failedStage] : []),
    ...stages
      .filter((stage) => FAILING_STAGE_STATUSES.has((stage.status ?? '').trim().toLowerCase()))
      .map((stage) => stage.stage),
  ]);
});
</script>

<template>
  <section v-if="result" class="preflight-panel" aria-labelledby="preflight-result-title">
    <h2 id="preflight-result-title">{{ t('operation.preflight.title') }}</h2>
    <p
      class="preflight-panel__overall"
      :class="`preflight-panel__overall--${overallView.tone}`"
      data-testid="preflight-overall"
      :data-tone="overallView.tone"
    >
      {{ overallView.label }}
      <span v-if="result.errorCode" class="preflight-panel__code">{{ result.errorCode }}</span>
    </p>
    <ul class="preflight-panel__stages">
      <li
        v-for="stage in result.stages"
        :key="stage.stage"
        class="preflight-stage"
        :class="{ 'preflight-stage--failed': failedStages.has(stage.stage) }"
        :data-testid="`preflight-stage-${stage.stage}`"
      >
        <span class="preflight-stage__name">{{ stage.stage }}</span>
        <span class="preflight-stage__status">{{ statusLabel('stage', stage.status) }}</span>
        <p v-if="stage.detail" class="preflight-stage__detail" data-testid="preflight-stage-detail">
          {{ stage.detail }}
        </p>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.preflight-panel__overall--passed {
  color: var(--color-success);
}

.preflight-panel__overall--failed {
  color: var(--color-error-strong);
}

.preflight-panel__overall--cancelled {
  color: var(--color-warning-ink-strong);
}

.preflight-panel__overall--unknown {
  color: var(--color-muted-strong);
}

.preflight-panel__code {
  margin-left: 0.5rem;
  font-family: var(--font-family-mono);
}

.preflight-stage {
  display: flex;
  gap: 0.5rem;
  align-items: baseline;
}

.preflight-stage--failed {
  background: var(--color-danger-surface);
  font-weight: 600;
}

.preflight-stage__detail {
  margin: 0;
  font-family: var(--font-family-mono);
}
</style>
