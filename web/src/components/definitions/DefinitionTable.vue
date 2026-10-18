<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
import type { DefinitionView } from '@/connect/definition-api';
import { formatTimestamp } from '@/utils/formatTimestamp';

/*
 * TASK-363: the definitions list, carved out of DefinitionsPage unchanged.
 *
 * Purely presentational: the page owns the store and decides what "edit" and
 * "disable" mean; this component only reports which row asked for them. The root
 * element is still the original `<table class="definitions__table">`, so the DOM
 * (columns, row testids, action buttons) is byte-for-byte the one the page rendered.
 */
defineProps<{
  definitions: DefinitionView[];
  canWrite: boolean;
  saving: boolean;
}>();

const emit = defineEmits<{
  edit: [definition: DefinitionView];
  disable: [definition: DefinitionView];
}>();
</script>

<template>
  <table class="definitions__table">
    <thead>
      <tr>
        <th scope="col">名称</th>
        <th scope="col">{{ t('definitions.span') }}</th>
        <th scope="col">{{ t('definitions.namespaceRelease') }}</th>
        <th scope="col">{{ t('definitions.chartName') }}</th>
        <th scope="col">状态</th>
        <th scope="col">版本</th>
        <th scope="col">{{ t('definitions.promotionMapping') }}</th>
        <th scope="col">操作</th>
      </tr>
    </thead>
    <tbody>
      <tr v-for="definition in definitions" :key="definition.id" :data-testid="`definition-row-${definition.id}`">
        <td>
          <strong>{{ definition.name }}</strong>
          <br />
          <small>{{ formatTimestamp(definition.updatedAt) }}</small>
        </td>
        <td>
          <code>{{ definition.customerId }}</code>
          <br />
          <small>{{ definition.clusterId }}</small>
        </td>
        <td>
          {{ definition.namespace }} / {{ definition.releaseName }}
        </td>
        <td>{{ definition.chartName || '—' }}</td>
        <td>{{ statusLabel('definition', definition.status) }}</td>
        <td>{{ definition.version }}</td>
        <td>
          <span v-if="definition.promotionMappingsViolation" class="definitions__bad">无法解析</span>
          <span v-else>{{ definition.promotionMappings.length }} 条</span>
        </td>
        <td>
          <div class="definitions__row-actions">
            <button
              v-if="canWrite"
              type="button"
              :disabled="Boolean(definition.promotionMappingsViolation)"
              :data-testid="`definition-edit-${definition.id}`"
              @click="emit('edit', definition)"
            >
              编辑
            </button>
            <!-- Only an active definition can be disabled; a disabled one is terminal
                 (the server has no re-enable procedure). -->
            <button
              v-if="canWrite && definition.status === 'active'"
              type="button"
              class="danger"
              :disabled="saving"
              :data-testid="`definition-disable-${definition.id}`"
              @click="emit('disable', definition)"
            >
              {{ t('definitions.disable.open') }}
            </button>
          </div>
        </td>
      </tr>
    </tbody>
  </table>
</template>

<style scoped>
.definitions__row-actions {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.definitions__row-actions button.danger {
  border-color: var(--color-danger-border);
  color: var(--color-error);
}

.definitions__table {
  width: 100%;
  border-collapse: collapse;
  background: var(--color-surface);
}

.definitions__table th,
.definitions__table td {
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border);
  text-align: left;
}

.definitions__table small {
  color: var(--color-muted);
}

.definitions__bad {
  color: var(--color-error);
  font-weight: var(--font-weight-bold);
}

.definitions__table button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}
</style>
