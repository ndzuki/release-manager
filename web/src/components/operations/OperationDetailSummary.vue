<script setup lang="ts">
import { statusLabel } from '@/i18n/status-labels';
import { t } from '@/i18n/messages';
import CopyableHash from '@/components/common/CopyableHash.vue';
import { formatTimestamp } from '@/utils/formatTimestamp';
import type { Operation } from '@/types/operation';

/*
 * The operation's summary block: the authoritative fields the timeline does not
 * repeat (identity, definition, versions, timestamps, and — for EMERGENCY operations —
 * the effect status the convergence action is keyed on).
 *
 * TASK-394: the operation id moved here from the old heading (the page title is now the
 * operation type plus release name), and both UUID-shaped values render through
 * CopyableHash — truncated on screen, full value in `title`, copyable.
 */
defineProps<{ operation: Operation | null }>();

// W17: detail timestamps keep second precision; formatting is shared.
function formatDetailTime(value: string | null): string {
  return formatTimestamp(value, { style: 'medium' });
}
</script>

<template>
  <dl class="operation-detail__summary">
    <div><dt>Operation ID</dt><dd><CopyableHash :value="operation?.operationId ?? ''" /></dd></div>
    <div><dt>ReleaseDefinition</dt><dd><CopyableHash :value="operation?.releaseDefinitionId ?? ''" /></dd></div>
    <div><dt>StateVersion</dt><dd>{{ operation?.stateVersion.toString() }}</dd></div>
    <div v-if="operation?.operationType === 'EMERGENCY'">
      <dt>{{ t('emergency.result.effectStatus') }}</dt><dd>{{ statusLabel('effect', operation?.effectStatus ?? undefined) }}</dd>
    </div>
    <div><dt>TargetRevision</dt><dd>{{ operation?.targetRevision || '—' }}</dd></div>
    <!--
      AC-058-48 / TASK-325 W3: only the server's redacted + truncated projection
      reaches the DOM; the raw reason is never part of the read model. An
      operation without a recorded reason renders no row at all.
    -->
    <div v-if="operation?.reasonDisplay" class="operation-detail__summary-reason">
      <dt>变更原因</dt><dd>{{ operation.reasonDisplay }}</dd>
    </div>
    <div><dt>创建时间</dt><dd>{{ formatDetailTime(operation?.createdAt ?? null) }}</dd></div>
    <div><dt>更新时间</dt><dd>{{ formatDetailTime(operation?.updatedAt ?? null) }}</dd></div>
    <div><dt>终止时间</dt><dd>{{ formatDetailTime(operation?.terminalAt ?? null) }}</dd></div>
  </dl>
</template>

<style scoped>
.operation-detail__summary { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); margin: 0; border: 1px solid var(--color-border-strong); border-radius: 0.7rem; background: var(--color-surface); }
.operation-detail__summary div { padding: 1rem; border-bottom: 1px solid var(--color-border); }
.operation-detail__summary dt { color: var(--color-muted); font-size: var(--font-size-sm); }
.operation-detail__summary dd { margin: 0.25rem 0 0; font-weight: 650; overflow-wrap: anywhere; }
.operation-detail__summary-reason { grid-column: 1 / -1; }
@media (max-width: 52rem) { .operation-detail__summary { grid-template-columns: 1fr; } }
</style>
