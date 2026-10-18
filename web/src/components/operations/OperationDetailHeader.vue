<script setup lang="ts">
import { statusLabel } from '@/i18n/status-labels';
import type { Operation } from '@/types/operation';

/*
 * Identity and actions of the operation detail page. Purely presentational: the page
 * owns the timeline store, this component owns the header's conditional actions and
 * reports what the operator asked for (cancel / refresh).
 */
defineProps<{
  operation: Operation | null;
  releaseName: string;
  showCancel: boolean;
  canCancel: boolean;
  cancelLoading: boolean;
  isTerminal: boolean;
}>();

const emit = defineEmits<{ 'request-cancel': []; refresh: [] }>();
</script>

<template>
  <header class="operation-detail__header">
    <div>
      <p class="operation-detail__eyebrow">{{ statusLabel('operationType', operation?.operationType) }}操作</p>
      <h1>{{ releaseName }}</h1>
      <p><code>{{ operation?.operationId }}</code></p>
    </div>
    <div class="operation-detail__header-actions">
      <span v-if="operation" class="operation-detail__state" :class="`operation-detail__state--${operation.state}`">
        {{ statusLabel('operation', operation.state) }}
      </span>
      <template v-if="showCancel">
        <button
          v-if="canCancel"
          type="button"
          class="operation-detail__cancel"
          :disabled="cancelLoading"
          @click="emit('request-cancel')"
        >
          {{ cancelLoading ? '取消中…' : '取消操作' }}
        </button>
        <button
          v-else-if="operation?.state === 'cancelling'"
          type="button"
          class="operation-detail__cancel operation-detail__cancel--disabled"
          disabled
        >
          取消中…
        </button>
        <button
          v-else-if="isTerminal"
          type="button"
          class="operation-detail__cancel operation-detail__cancel--disabled"
          disabled
          title="操作已完成，无法取消"
        >
          取消操作
        </button>
      </template>
      <button
        v-if="operation"
        type="button"
        class="operation-detail__refresh"
        @click="emit('refresh')"
      >
        刷新
      </button>
    </div>
  </header>
</template>

<style scoped>
.operation-detail__header { display: flex; justify-content: space-between; align-items: flex-start; gap: 1rem; }
.operation-detail__header h1, .operation-detail__header p { margin: 0; }
.operation-detail__eyebrow { color: var(--color-primary); font-size: var(--font-size-xs); font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.operation-detail__state { padding: 0.45rem 0.7rem; border-radius: 999px; background: var(--color-border); text-transform: uppercase; font-size: var(--font-size-xs); font-weight: 800; }
.operation-detail__state--failed, .operation-detail__state--timeout { background: var(--color-danger-surface); color: var(--color-error); }
.operation-detail__state--succeeded { background: var(--color-success-surface); color: var(--color-success-ink); }
.operation-detail__header-actions { display: flex; align-items: center; gap: 0.75rem; }
.operation-detail__cancel { min-height: 2.4rem; padding: 0.45rem 0.85rem; border: 1px solid var(--color-danger); border-radius: 0.45rem; background: var(--color-surface); color: var(--color-danger); cursor: pointer; font-weight: 700; }
.operation-detail__cancel--disabled { border-color: var(--color-border-strong); color: var(--color-subtle); cursor: not-allowed; }
.operation-detail__refresh { min-height: 2.4rem; padding: 0.45rem 0.85rem; border: 1px solid var(--color-border-strong); border-radius: 0.45rem; background: var(--color-surface); color: var(--color-text-secondary); cursor: pointer; font-weight: 600; }
</style>
