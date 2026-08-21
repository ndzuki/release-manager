<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useRoute } from 'vue-router';
import CancelOperationDialog from '@/components/operations/CancelOperationDialog.vue';
import DisconnectBanner from '@/components/operations/DisconnectBanner.vue';
import OperationTimeline from '@/components/operations/OperationTimeline.vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import { useOperationTimelineStore } from '@/stores/operationTimeline';

const route = useRoute();
const store = useOperationTimelineStore();
const operationId = computed(() => String(route.params.operationId ?? ''));
const releaseName = computed(() => String(route.query.releaseName ?? route.params.releaseId ?? 'Release'));
// Full route scope: a same operationId under a different customer/cluster/
// release must reset the store and open a fresh stream (AC-057-15).
const routeScope = computed(() =>
  [route.params.customerId, route.params.clusterId, route.params.releaseId, route.params.operationId]
    .map(String)
    .join('/'),
);

const liveUpdatesEnabled = import.meta.env.VITE_OPERATION_LIVE_UPDATES !== 'false';

// Configure store-level seams once (production defaults, no-ops).
store.configure({ liveUpdatesEnabled: () => liveUpdatesEnabled });

const cancelTrigger = ref<HTMLButtonElement | null>(null);
let lastFocused: HTMLElement | null = null;
// a11y (grilling v15): return focus to the trigger when the dialog closes.
watch(
  () => store.cancelDialogOpen,
  (open, wasOpen) => {
    if (open) {
      lastFocused = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    } else if (wasOpen && lastFocused) {
      lastFocused.focus();
      lastFocused = null;
    }
  },
);

watch(routeScope, (current, previous) => {
  const nextOperationId = String(route.params.operationId ?? '');
  if (!nextOperationId) return;
  if (previous && current !== previous) {
    // Scope changed (possibly with the same operationId): reset first so the
    // load() early-return guard cannot absorb a same-id navigation
    // (AC-057-15: old stream/timers must stop before the new scope loads).
    store.reset();
  }
  void store.load(nextOperationId);
}, { immediate: true });
onBeforeUnmount(store.reset);

function formatTimestamp(value: string | null): string {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' }).format(new Date(value)) : '—';
}
</script>

<template>
  <section class="operation-detail">
    <nav class="operation-detail__breadcrumbs" aria-label="Breadcrumb">
      <RouterLink
        :to="{
          name: 'ReleaseInventory',
          params: { customerId: route.params.customerId, clusterId: route.params.clusterId },
        }"
      >
        Releases
      </RouterLink>
      <span aria-hidden="true">/</span><span>{{ releaseName }}</span><span aria-hidden="true">/</span>
      <strong>{{ operationId }}</strong>
    </nav>

    <LoadingState
      v-if="!store.operation && !store.initialError && liveUpdatesEnabled"
      message="正在加载 Operation…"
    />
    <EmptyState
      v-else-if="!store.operation && !store.initialError"
      title="实时更新已关闭"
      message="点击刷新加载 Operation 最新状态"
      action-label="刷新"
      @action="() => void store.refresh()"
    />
    <ErrorState
      v-else-if="!store.operation && store.initialError"
      title="Operation 加载失败"
      :message="store.initialError.message"
      :action-label="store.initialError.retryable ? '重试' : ''"
      @action="store.retryInitial"
    />
    <template v-else>
      <header class="operation-detail__header">
        <div>
          <p class="operation-detail__eyebrow">{{ store.operation?.operationType }} operation</p>
          <h1>{{ releaseName }}</h1>
          <p><code>{{ store.operation?.operationId }}</code></p>
        </div>
        <div class="operation-detail__header-actions">
          <span v-if="store.operation" class="operation-detail__state" :class="`operation-detail__state--${store.operation.state}`">
            {{ store.operation.state }}
          </span>
          <template v-if="store.showCancel">
            <button
              v-if="store.canCancel"
              ref="cancelTrigger"
              type="button"
              class="operation-detail__cancel"
              :disabled="store.cancelLoading"
              @click="store.cancelDialogOpen = true"
            >
              {{ store.cancelLoading ? '取消中…' : '取消操作' }}
            </button>
            <button
              v-else-if="store.operation?.state === 'cancelling'"
              type="button"
              class="operation-detail__cancel operation-detail__cancel--disabled"
              disabled
            >
              取消中…
            </button>
            <button
              v-else-if="store.isTerminal"
              type="button"
              class="operation-detail__cancel operation-detail__cancel--disabled"
              disabled
              title="操作已完成，无法取消"
            >
              取消操作
            </button>
          </template>
          <button
            v-if="store.operation"
            type="button"
            class="operation-detail__refresh"
            @click="() => void store.refresh()"
          >
            刷新
          </button>
        </div>
      </header>

      <DisconnectBanner :visible="store.streamStatus === 'disconnected'" />

      <dl class="operation-detail__summary">
        <div><dt>ReleaseDefinition</dt><dd>{{ store.operation?.releaseDefinitionId }}</dd></div>
        <div><dt>StateVersion</dt><dd>{{ store.operation?.stateVersion.toString() }}</dd></div>
        <div v-if="store.operation?.operationType === 'EMERGENCY'">
          <dt>EffectStatus</dt><dd>{{ store.operation?.effectStatus }}</dd>
        </div>
        <div><dt>TargetRevision</dt><dd>{{ store.operation?.targetRevision || '—' }}</dd></div>
        <div><dt>创建时间</dt><dd>{{ formatTimestamp(store.operation?.createdAt ?? null) }}</dd></div>
        <div><dt>更新时间</dt><dd>{{ formatTimestamp(store.operation?.updatedAt ?? null) }}</dd></div>
        <div><dt>终止时间</dt><dd>{{ formatTimestamp(store.operation?.terminalAt ?? null) }}</dd></div>
      </dl>

      <OperationTimeline
        :entries="store.entries"
        :operation="store.operation"
        :stream-status="store.streamStatus"
        :history-truncated="store.historyTruncated"
        :history-gap="store.historyGap"
        :emergency-effect-status="store.emergencyEffectStatus"
      />

      <CancelOperationDialog
        v-if="store.cancelDialogOpen"
        :submitting="store.cancelLoading"
        :error="store.cancelError"
        :emergency-queued="store.operation?.operationType === 'EMERGENCY' && store.operation?.state === 'queued'"
        @submit="(reason) => void store.submitCancel(reason).then((result) => { if (result.ok) store.cancelDialogOpen = false; })"
        @close="store.cancelDialogOpen = false"
      />
    </template>
  </section>
</template>

<style scoped>
.operation-detail { display: grid; gap: 1.5rem; max-width: 70rem; margin: 0 auto; }
.operation-detail__breadcrumbs { display: flex; flex-wrap: wrap; gap: 0.45rem; color: #64748b; font-size: 0.85rem; }
.operation-detail__breadcrumbs a { color: #2563eb; }
.operation-detail__header { display: flex; justify-content: space-between; align-items: flex-start; gap: 1rem; }
.operation-detail__header h1, .operation-detail__header p { margin: 0; }
.operation-detail__eyebrow { color: #2563eb; font-size: 0.75rem; font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.operation-detail__state { padding: 0.45rem 0.7rem; border-radius: 999px; background: #e2e8f0; text-transform: uppercase; font-size: 0.75rem; font-weight: 800; }
.operation-detail__state--failed, .operation-detail__state--timeout { background: #fee2e2; color: #b91c1c; }
.operation-detail__state--succeeded { background: #dcfce7; color: #166534; }
.operation-detail__header-actions { display: flex; align-items: center; gap: 0.75rem; }
.operation-detail__cancel { min-height: 2.4rem; padding: 0.45rem 0.85rem; border: 1px solid #dc2626; border-radius: 0.45rem; background: #fff; color: #dc2626; cursor: pointer; font-weight: 700; }
.operation-detail__cancel--disabled { border-color: #cbd5e1; color: #94a3b8; cursor: not-allowed; }
.operation-detail__refresh { min-height: 2.4rem; padding: 0.45rem 0.85rem; border: 1px solid #cbd5e1; border-radius: 0.45rem; background: #fff; color: #334155; cursor: pointer; font-weight: 600; }
.operation-detail__summary { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); margin: 0; border: 1px solid #cbd5e1; border-radius: 0.7rem; background: #fff; }
.operation-detail__summary div { padding: 1rem; border-bottom: 1px solid #e2e8f0; }
.operation-detail__summary dt { color: #64748b; font-size: 0.8rem; }
.operation-detail__summary dd { margin: 0.25rem 0 0; font-weight: 650; overflow-wrap: anywhere; }
@media (max-width: 52rem) { .operation-detail__summary { grid-template-columns: 1fr; } }
</style>
