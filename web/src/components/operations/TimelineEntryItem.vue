<script setup lang="ts">
import { onUnmounted, ref } from 'vue';
import type { TimelineEntry } from '@/types/operation';

defineProps<{ entry: TimelineEntry }>();

const copied = ref(false);
let copiedResetTimer: ReturnType<typeof setTimeout> | null = null;

onUnmounted(() => {
  if (copiedResetTimer !== null) clearTimeout(copiedResetTimer);
});

function formatTimestamp(value: string | null): string {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' }).format(new Date(value)) : '—';
}

async function copyIdentity(value: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(value);
    copied.value = true;
    // Replace any pending reset so a rapid second copy keeps the indicator
    // visible for the full duration of the latest click (AC-057-04 UX).
    if (copiedResetTimer !== null) clearTimeout(copiedResetTimer);
    copiedResetTimer = setTimeout(() => {
      copied.value = false;
      copiedResetTimer = null;
    }, 1200);
  } catch {
    // Clipboard unavailable (permissions/HTTP): leave the selectable text in place.
  }
}

const kindLabel: Record<string, string> = {
  STATE_TRANSITION: '状态转换',
  ACK: '已确认',
  ROLLOUT_PROGRESS: '发布进度',
  ERROR: '错误',
  EMERGENCY_EFFECT_RESOLVED: 'Emergency 生效结果已确认',
  UNSPECIFIED: '未知事件',
};
</script>

<template>
  <li class="timeline-entry" :class="`timeline-entry--${entry.kind.toLowerCase()}`" tabindex="0" :aria-label="kindLabel[entry.kind] ?? entry.kind">
    <span class="timeline-entry__dot" aria-hidden="true" />
    <div class="timeline-entry__body">
      <div class="timeline-entry__header">
        <strong>{{ kindLabel[entry.kind] ?? entry.kind }}</strong>
        <time :datetime="entry.timestamp ?? ''">{{ formatTimestamp(entry.timestamp) }}</time>
      </div>

      <template v-if="entry.kind === 'STATE_TRANSITION'">
        <p class="timeline-entry__text">
          {{ entry.fromState || '未知状态' }} → {{ entry.toState || '未知状态' }}
        </p>
      </template>

      <template v-else-if="entry.kind === 'ACK'">
        <p class="timeline-entry__text">{{ entry.ackStage ? `ACK 已确认（${entry.ackStage}）` : 'ACK 已确认' }}</p>
      </template>

      <template v-else-if="entry.kind === 'ROLLOUT_PROGRESS'">
        <p class="timeline-entry__text">
          {{ entry.workloadRef || 'Workload' }}：{{ entry.ready }}/{{ entry.desired }} 就绪
        </p>
      </template>

      <template v-else-if="entry.kind === 'ERROR'">
        <p class="timeline-entry__text">{{ entry.errorMessage || entry.errorCode || '发布出错' }}</p>
        <p v-if="entry.errorCode" class="timeline-entry__code">错误码：{{ entry.errorCode }}</p>
        <div v-if="entry.operationId || entry.requestId" class="timeline-entry__identities">
          <button
            v-if="entry.operationId"
            type="button"
            class="timeline-entry__copy"
            :aria-label="`复制 Operation ID ${entry.operationId}`"
            @click="copyIdentity(entry.operationId)"
          >
            <code>{{ entry.operationId }}</code>{{ copied ? ' ✓' : ' 复制' }}
          </button>
          <button
            v-if="entry.requestId"
            type="button"
            class="timeline-entry__copy"
            :aria-label="`复制 Request ID ${entry.requestId}`"
            @click="copyIdentity(entry.requestId)"
          >
            <code>{{ entry.requestId }}</code>{{ copied ? ' ✓' : ' 复制' }}
          </button>
        </div>
      </template>

      <template v-else-if="entry.kind === 'EMERGENCY_EFFECT_RESOLVED'">
        <p class="timeline-entry__text">
          {{ entry.effectFrom || entry.fromState || 'UNKNOWN' }} → {{ entry.effectTo || entry.toState || '未知' }}
        </p>
      </template>
    </div>
  </li>
</template>

<style scoped>
.timeline-entry { display: flex; gap: 0.75rem; padding: 0.75rem 1rem; border: 1px solid #e2e8f0; border-radius: 0.6rem; background: #fff; }
.timeline-entry:focus-visible { outline: 2px solid #2563eb; outline-offset: 2px; }
.timeline-entry__dot { flex: none; width: 0.6rem; height: 0.6rem; margin-top: 0.35rem; border-radius: 50%; background: #94a3b8; }
.timeline-entry--state_transition .timeline-entry__dot { background: #2563eb; }
.timeline-entry--ack .timeline-entry__dot { background: #0891b2; }
.timeline-entry--rollout_progress .timeline-entry__dot { background: #ca8a04; }
.timeline-entry--error .timeline-entry__dot { background: #dc2626; }
.timeline-entry--emergency_effect_resolved .timeline-entry__dot { background: #2563eb; }
.timeline-entry__body { display: grid; gap: 0.3rem; min-width: 0; }
.timeline-entry__header { display: flex; flex-wrap: wrap; gap: 0.5rem; align-items: baseline; }
.timeline-entry__header time { color: #64748b; font-size: 0.8rem; }
.timeline-entry__text { margin: 0; color: #334155; font-size: 0.9rem; overflow-wrap: anywhere; }
.timeline-entry__code { margin: 0; color: #b91c1c; font-size: 0.8rem; }
.timeline-entry__identities { display: flex; flex-wrap: wrap; gap: 0.5rem; }
.timeline-entry__copy { padding: 0.25rem 0.5rem; border: 1px solid #cbd5e1; border-radius: 0.35rem; background: #fff; color: #1d4ed8; font-size: 0.78rem; cursor: pointer; }
.timeline-entry--error { border-color: #fecaca; background: #fef2f2; }
.timeline-entry--emergency_effect_resolved { border-color: #bfdbfe; background: #eff6ff; }
</style>
