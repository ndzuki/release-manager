<script setup lang="ts">
import { computed } from 'vue';
import { t } from '@/i18n/messages';
import EmptyState from '@/components/common/EmptyState.vue';
import { useVirtualList } from '@/composables/useVirtualList';
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

/*
 * W15 (TASK-325): the stream keeps at most 500 entries, and rendering every one of
 * them keeps the main thread busy on long operations. Above the virtual-list
 * threshold only the rows around the viewport stay in the DOM; below it the list
 * renders exactly as before. The payload is still the whole retained history —
 * bounding it needs a server-side cursor (see the W15 row in delivery-evidence.md).
 */
const {
  containerRef: listContainer,
  virtualized,
  visible,
  startIndex,
  topSpacer,
  bottomSpacer,
  onScroll,
  rowRef,
} = useVirtualList(() => sortedEntries.value, {
  keyOf: (entry) => entry.id,
  // .operation-timeline__list { gap: 0.5rem }
  gap: 8,
});

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

    <!-- The scroll container is the wrapper, not the list: the window's spacers are
         padding on the list, and padding is a floor under a border-box element — a
         max-height on the padded list would be ignored and every entry would render. -->
    <div
      v-else
      ref="listContainer"
      class="operation-timeline__viewport"
      :class="{ 'operation-timeline__viewport--virtual': virtualized }"
      :role="virtualized ? 'region' : undefined"
      :tabindex="virtualized ? 0 : undefined"
      :aria-label="virtualized ? t('operation.timeline.scrollRegion') : undefined"
      @scroll.passive="onScroll"
    >
      <ol
        class="operation-timeline__list"
        aria-live="polite"
        :style="virtualized ? { paddingTop: `${topSpacer}px`, paddingBottom: `${bottomSpacer}px` } : undefined"
      >
        <TimelineEntryItem
          v-for="(entry, index) in visible"
          :key="entry.id"
          :ref="rowRef(entry.id)"
          :entry="entry"
          :aria-setsize="virtualized ? sortedEntries.length : undefined"
          :aria-posinset="virtualized ? startIndex + index + 1 : undefined"
        />
      </ol>
    </div>

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
.operation-timeline h2 { margin: 0; font-size: var(--font-size-base); }
.operation-timeline__list { display: grid; gap: 0.5rem; padding: 0; margin: 0; list-style: none; }
/* W15: only a list long enough to be virtualized becomes its own scroll container,
   so an ordinary operation detail page keeps scrolling as one page. */
.operation-timeline__viewport--virtual { max-height: min(60vh, 32rem); overflow-y: auto; overscroll-behavior: contain; scrollbar-gutter: stable; }
.operation-timeline__viewport--virtual:focus-visible { outline: 2px solid var(--color-primary); outline-offset: 2px; }
.operation-timeline__gap,
.operation-timeline__truncated { margin: 0; padding: 0.5rem 0.75rem; border-radius: 0.4rem; background: var(--color-warning-surface); color: var(--color-warning-ink-strong); font-size: var(--font-size-sm); }
.operation-timeline__truncated { background: var(--color-bg); color: var(--color-muted); }
.operation-timeline__waiting { margin: 0; padding: 0.6rem 0.75rem; border: 1px solid var(--color-info-border-soft); border-radius: 0.5rem; background: var(--color-info-soft); color: var(--color-primary-hover); font-size: var(--font-size-sm); }
</style>
