<script setup lang="ts">
import { computed } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import TimelineEntryItem from '@/components/operations/TimelineEntryItem.vue';
import type { Operation, TimelineEntry } from '@/types/operation';
import type { EmergencyEffectStatus, StreamStatus } from '@/stores/operationTimeline';

const props = defineProps<{
  entries: TimelineEntry[];
  operation: Operation | null;
  streamStatus: StreamStatus;
  historyTruncated?: boolean;
  historyGap?: boolean;
  emergencyEffectStatus?: EmergencyEffectStatus;
}>();

const sortedEntries = computed(() =>
  [...props.entries].sort((a, b) => (a.sequence < b.sequence ? 1 : a.sequence > b.sequence ? -1 : 0)),
);

const emergencyWaiting = computed(() => {
  const current = props.operation;
  return (
    current?.operationType === 'EMERGENCY' &&
    current.state === 'cancelled' &&
    current.effectStatus === 'UNKNOWN' &&
    props.emergencyEffectStatus === 'watching'
  );
});

// AC-057-20: once the EMERGENCY_EFFECT_RESOLVED entry arrives, the waiting
// note is replaced by the outcome text derived from the effect transition.
const emergencyResolved = computed(() => {
  if (props.emergencyEffectStatus !== 'resolved') return '';
  const resolved = [...props.entries]
    .reverse()
    .find((entry) => entry.kind === 'EMERGENCY_EFFECT_RESOLVED');
  if (!resolved) return '操作已取消';
  return resolved.effectTo === 'APPLIED' ? '操作已取消' : '操作已取消，变更未生效';
});
</script>

<template>
  <section class="operation-timeline" aria-labelledby="operation-timeline-title">
    <h2 id="operation-timeline-title">状态时间线</h2>

    <p v-if="historyGap" class="operation-timeline__gap" role="alert">部分历史事件不可用</p>
    <p v-if="historyTruncated" class="operation-timeline__truncated">更早的事件已超出显示范围（最多 500 条）</p>

    <EmptyState v-if="entries.length === 0" title="暂无时间线事件" message="等待服务端推送事件…" />

    <ol v-else class="operation-timeline__list">
      <TimelineEntryItem v-for="entry in sortedEntries" :key="entry.id" :entry="entry" />
    </ol>

    <p v-if="emergencyWaiting" class="operation-timeline__waiting" role="status">
      操作已取消，正在等待集群生效结果确认…
    </p>
    <p v-else-if="emergencyResolved" class="operation-timeline__waiting" role="status">
      {{ emergencyResolved }}
    </p>
  </section>
</template>

<style scoped>
.operation-timeline { display: grid; gap: 0.75rem; }
.operation-timeline h2 { margin: 0; font-size: 1rem; }
.operation-timeline__list { display: grid; gap: 0.5rem; padding: 0; margin: 0; list-style: none; }
.operation-timeline__gap,
.operation-timeline__truncated { margin: 0; padding: 0.5rem 0.75rem; border-radius: 0.4rem; background: #fffbeb; color: #92400e; font-size: 0.85rem; }
.operation-timeline__truncated { background: #f8fafc; color: #64748b; }
.operation-timeline__waiting { margin: 0; padding: 0.6rem 0.75rem; border: 1px solid #bfdbfe; border-radius: 0.5rem; background: #eff6ff; color: #1d4ed8; font-size: 0.85rem; }
</style>
