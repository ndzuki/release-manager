<script setup lang="ts">
import { computed, onBeforeUnmount, watch } from 'vue';
import { useRoute } from 'vue-router';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import OperationTimeline from '@/components/operations/OperationTimeline.vue';
import { usePreflightStore } from '@/stores/preflight';

const route = useRoute();
const store = usePreflightStore();
const operationId = computed(() => String(route.params.operationId ?? ''));
const releaseName = computed(() => String(route.query.releaseName ?? route.params.releaseId ?? 'Release'));

watch(operationId, (nextOperationId) => void store.load(nextOperationId), { immediate: true });
onBeforeUnmount(store.stopPolling);

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

    <LoadingState v-if="!store.operation && !store.error" message="正在加载 Operation…" />
    <ErrorState
      v-else-if="!store.operation"
      title="Operation 加载失败"
      :message="store.error ?? ''"
      action-label="重试"
      @action="store.refresh"
    />
    <template v-else>
      <header class="operation-detail__header">
        <div>
          <p class="operation-detail__eyebrow">{{ store.operation.operationType }} operation</p>
          <h1>{{ releaseName }}</h1>
          <p><code>{{ store.operation.operationId }}</code></p>
        </div>
        <span class="operation-detail__state" :class="`operation-detail__state--${store.operation.state}`">
          {{ store.operation.state }}
        </span>
      </header>

      <div v-if="store.error" class="operation-detail__network" role="alert">
        {{ store.error }}。保留最后一次服务端状态。
        <button type="button" @click="store.refresh">重试</button>
      </div>

      <dl class="operation-detail__summary">
        <div><dt>ReleaseDefinition</dt><dd>{{ store.operation.releaseDefinitionId }}</dd></div>
        <div><dt>Bundle</dt><dd>{{ store.operation.bundleId || '—' }}</dd></div>
        <div><dt>ValuesRevision</dt><dd>{{ store.operation.valuesRevisionId || '—' }}</dd></div>
        <div><dt>ExpectedRevision</dt><dd>{{ store.operation.expectedRevision || '—' }}</dd></div>
        <div><dt>TargetRevision</dt><dd>{{ store.operation.targetRevision || '—' }}</dd></div>
        <div><dt>StateVersion</dt><dd>{{ store.operation.stateVersion }}</dd></div>
        <div><dt>创建时间</dt><dd>{{ formatTimestamp(store.operation.createdAt) }}</dd></div>
        <div><dt>更新时间</dt><dd>{{ formatTimestamp(store.operation.updatedAt) }}</dd></div>
        <div><dt>终止时间</dt><dd>{{ formatTimestamp(store.operation.terminalAt) }}</dd></div>
        <div><dt>截止时间</dt><dd>{{ formatTimestamp(store.operation.deadline) }}</dd></div>
      </dl>

      <OperationTimeline :operation="store.operation" />

      <ErrorState
        v-if="store.operation.lastError"
        title="Operation 失败"
        :message="store.operation.lastError"
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
.operation-detail__summary { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); margin: 0; border: 1px solid #cbd5e1; border-radius: 0.7rem; background: #fff; }
.operation-detail__summary div { padding: 1rem; border-bottom: 1px solid #e2e8f0; }
.operation-detail__summary dt { color: #64748b; font-size: 0.8rem; }
.operation-detail__summary dd { margin: 0.25rem 0 0; font-weight: 650; overflow-wrap: anywhere; }
.operation-detail__network { display: flex; align-items: center; justify-content: space-between; gap: 1rem; padding: 0.8rem 1rem; border: 1px solid #fdba74; border-radius: 0.5rem; background: #fff7ed; color: #9a3412; }
.operation-detail__network button { padding: 0.45rem 0.7rem; border: 1px solid #fdba74; border-radius: 0.35rem; background: #fff; }
@media (max-width: 52rem) { .operation-detail__summary { grid-template-columns: 1fr; } }
</style>
