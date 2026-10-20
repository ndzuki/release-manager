<script setup lang="ts">
import type { Operation } from '@/types/operation';

/*
 * The operation detail page's mutating entries: cancel, and the manual refresh. Purely
 * presentational — the page owns the timeline store and acts on what this reports.
 *
 * TASK-394: the direction-C PageHeader owns the heading (operation type + release name)
 * and the subtitle (state + version), so this component no longer renders an h1, the
 * eyebrow or the raw operation id. It is mounted in PageHeader's `#actions` slot and
 * keeps the conditional contract the page already relied on (can cancel / already
 * cancelling / terminal / no write permission).
 */
defineProps<{
  operation: Operation | null;
  showCancel: boolean;
  canCancel: boolean;
  cancelLoading: boolean;
  isTerminal: boolean;
}>();

const emit = defineEmits<{ 'request-cancel': []; refresh: [] }>();
</script>

<template>
  <div class="operation-detail__header-actions">
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
</template>

<style scoped>
.operation-detail__header-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 0.75rem; }
/* Outlined danger button: the rail keeps the palette base, the label takes the readable
   step (TASK-394). Both resolve through `--er`, so the border still frames the label. */
.operation-detail__cancel { min-height: 2.4rem; padding: 0.45rem 0.85rem; border: 1px solid var(--color-danger); border-radius: 0.45rem; background: var(--color-surface); color: var(--color-error-strong); cursor: pointer; font-weight: 700; }
.operation-detail__cancel--disabled { border-color: var(--color-border-strong); color: var(--color-subtle); cursor: not-allowed; }
.operation-detail__refresh { min-height: 2.4rem; padding: 0.45rem 0.85rem; border: 1px solid var(--color-border-strong); border-radius: 0.45rem; background: var(--color-surface); color: var(--color-text-secondary); cursor: pointer; font-weight: 600; }
</style>
