<script setup lang="ts">
import type { PreflightResult } from '@/types/operation';

// Preflight stage results (TASK-149 / REQ-056 AC-056-03): highlight the stage
// that failed and expand its checks. The panel renders nothing while preflight
// is still in flight, so the caller can mount it unconditionally.
defineProps<{ result: PreflightResult | null }>();
</script>

<template>
  <section v-if="result" class="preflight-panel" aria-labelledby="preflight-result-title">
    <h2 id="preflight-result-title">Preflight</h2>
    <p class="preflight-panel__overall" :class="{ 'preflight-panel__overall--failed': result.overall === 'failed' }">
      {{ result.overall === 'failed' ? '失败' : '通过' }}
      <span v-if="result.errorCode" class="preflight-panel__code">{{ result.errorCode }}</span>
    </p>
    <ul class="preflight-panel__stages">
      <li
        v-for="stage in result.stages"
        :key="stage.stage"
        class="preflight-stage"
        :class="{ 'preflight-stage--failed': stage.stage === result.failedStage }"
        :data-testid="`preflight-stage-${stage.stage}`"
      >
        <span class="preflight-stage__name">{{ stage.stage }}</span>
        <span class="preflight-stage__status">{{ stage.status }}</span>
        <p v-if="stage.detail" class="preflight-stage__detail" data-testid="preflight-stage-detail">
          {{ stage.detail }}
        </p>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.preflight-panel__overall--failed {
  color: #991b1b;
}

.preflight-panel__code {
  margin-left: 0.5rem;
  font-family: monospace;
}

.preflight-stage {
  display: flex;
  gap: 0.5rem;
  align-items: baseline;
}

.preflight-stage--failed {
  background: #fee2e2;
  font-weight: 600;
}

.preflight-stage__detail {
  margin: 0;
  font-family: monospace;
}
</style>
