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
  <!--
    TASK-367: eight columns of names, namespace/release pairs and chart names have a
    min-content width well above a 375px viewport. The table is wrapped in a labelled,
    focusable scroll region (the same shape as AuditEventTable and ReleaseInventoryTable)
    instead of letting the whole page scroll sideways, so a keyboard user can Tab to the
    region and scroll it with the arrow keys. TASK-394: the region is the shared
    `.table-wrap` instead of a page-private scroll class, and it now carries the frame.
  -->
  <div
    class="table-wrap"
    role="region"
    :aria-label="t('definitions.table.scrollRegion')"
    tabindex="0"
    data-testid="definitions-table-scroll"
  >
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
  </div>
</template>

<style scoped>
/* Direction C table frame: the surface is `--sf`, so the strong border is what separates
   the table from the page (the plain `--bd` hairline measured ~1.3:1 and read as a flat
   sheet). Horizontal containment comes from the global `.table-wrap` rule. */
.table-wrap {
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--color-surface);
}

.definitions__row-actions {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

/* The action cell is the narrowest column; without this the two labels break mid-word
   ("编 / 辑") once the cell padding grew to the direction-C 10px 20px. */
.definitions__row-actions button {
  white-space: nowrap;
}

.definitions__row-actions button.danger {
  border-color: var(--color-danger-border);
  color: var(--color-error-strong);
}

.definitions__table {
  width: 100%;
  border-collapse: collapse;
  background: var(--color-surface);
}

.definitions__table th,
.definitions__table td {
  padding: 10px 20px;
  border-bottom: 1px solid var(--color-border-strong);
  text-align: left;
}

/* Direction-C header: the muted surface and the strong muted ink separate the header row
   from the body instead of relying on the UA bold. The labels stay on one line: the 20px
   cell padding plus eight columns otherwise squeezed "版本" into one glyph per line. */
.definitions__table th {
  background: var(--color-surface-muted);
  color: var(--color-muted-strong);
  font-size: var(--font-size-xs);
  font-weight: 600;
  letter-spacing: 0.04em;
  white-space: nowrap;
}

/* The status label is three characters; the column is narrow once the UUID column claims
   its width, so it stays on one line instead of breaking into "生效 / 中". */
.definitions__table td:nth-child(5) {
  white-space: nowrap;
}

.definitions__table small {
  color: var(--color-muted);
}

.definitions__bad {
  color: var(--color-error-strong);
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
