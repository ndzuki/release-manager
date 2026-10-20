<script setup lang="ts">
import { t } from '@/i18n/messages';
import type { BindingView } from '@/connect/binding-api';
import { formatTimestamp } from '@/utils/formatTimestamp';

/*
 * TASK-363: the bindings table, carved out of BindingsPage unchanged.
 *
 * Purely presentational: the page owns the store and decides what "revoke" means;
 * this component only reports which row asked for it. The root element is still
 * the original `<table class="governance__table">`, so the DOM (columns, row
 * testids, status classes, action cell) is the one the page rendered.
 *
 * ListBindings includes revoked rows on purpose (history is kept), so the table
 * shows the status rather than filtering them out, and only an active row offers
 * the destructive action.
 */
defineProps<{
  bindings: BindingView[];
  saving: boolean;
}>();

const emit = defineEmits<{
  revoke: [binding: BindingView];
}>();

function statusLabel(binding: BindingView): string {
  return binding.status === 'active' ? '生效中' : '已撤销';
}
</script>

<template>
  <div
    class="governance__scroll"
    role="region"
    :aria-label="t('governance.table.scrollRegion')"
    tabindex="0"
  >
    <table class="governance__table">
      <thead>
        <tr>
          <th scope="col">客户</th>
          <th scope="col">状态</th>
          <th scope="col">版本</th>
          <th scope="col">建立时间</th>
          <th scope="col">操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="binding in bindings" :key="binding.id" :data-testid="`binding-row-${binding.customerId}`">
          <td><code>{{ binding.customerId }}</code></td>
          <td>
            <span class="governance__status" :class="`governance__status--${binding.status}`">{{ statusLabel(binding) }}</span>
          </td>
          <td>{{ binding.optimisticVersion }}</td>
          <td>{{ formatTimestamp(binding.createdAt) }}</td>
          <td>
            <button
              v-if="binding.status === 'active'"
              type="button"
              class="danger"
              :disabled="saving"
              @click="emit('revoke', binding)"
            >
              撤销
            </button>
            <span v-else class="governance__muted">—</span>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.governance__scroll {
  overflow-x: auto;
  min-width: 0;
}

.governance__table {
  width: 100%;
  border: 1px solid var(--color-border-strong);
  border-collapse: collapse;
  background: var(--color-surface);
}

.governance__table th,
.governance__table td {
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border);
  text-align: left;
}

.governance__status {
  padding: 0.1rem var(--space-2);
  border-radius: var(--radius-xl);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
}

.governance__status--active {
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

.governance__status--revoked {
  background: var(--color-surface-muted);
  color: var(--color-muted);
}

.governance__muted {
  color: var(--color-muted);
}

.governance__table button.danger {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-danger-border);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  color: var(--color-error);
  cursor: pointer;
}
</style>
