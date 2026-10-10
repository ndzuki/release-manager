<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
import DataTable from '@/components/common/DataTable.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import { useOperationFeed } from '@/composables/useOperationFeed';

/*
 * Operation centre (REQ-100 handoff H9 / TASK-277).
 *
 * Before TASK-276 the only operation list was per ReleaseDefinition, so "what is
 * waiting right now, across the whole organization?" had no answer. This page reads the
 * cross-release aggregate and renders it through the shared DataTable primitive.
 *
 * Three things the contract makes this page responsible for:
 *   - the ORDER is the server's (`created_at ASC, id ASC`, longest wait first) and it
 *     spans pages, while DataTable's sortable headers only reorder the rows in hand —
 *     so the sortable columns are marked as a within-page convenience, never as the
 *     queue's order;
 *   - paging is by OPAQUE keyset cursor: there is no offset and no total, so "next" is
 *     offered only while the server sent a next_page_token and "previous" replays the
 *     cursor stack already walked;
 *   - a refusal is a first-class state, not an empty list: maintenance mode answers
 *     UNAVAILABLE (`maintenance`) and an out-of-scope request answers PERMISSION_DENIED.
 */
const PAGE_SIZE = 20;

const { items, nextPageToken, loading, error, maintenance, forbidden, load } =
  useOperationFeed({ pageSize: PAGE_SIZE });

/*
 * Cursor stack. index 0 is the first page (empty cursor); going back replays the token
 * that produced the previous page, and going forward again overwrites the entry for the
 * position being entered, because the token for THAT position is the newest one.
 */
const cursors = ref<string[]>(['']);
const cursorIndex = ref(0);

const hasPrev = computed(() => cursorIndex.value > 0);
const hasNext = computed(() => nextPageToken.value !== '');
const pageNumber = computed(() => cursorIndex.value + 1);
// The pager stays reachable after an empty page (the user must be able to walk back)
// but is meaningless during a failure.
const showPager = computed(
  () =>
    !maintenance.value &&
    !forbidden.value &&
    !error.value &&
    (items.value.length > 0 || hasPrev.value),
);

/*
 * Column keys, referenced from the template through this constant instead of as
 * template literals: the copy gate reads ANY capitalised quoted token inside a
 * template as user-visible copy (web/src/i18n/copy-lint.ts), and `column.key ===
 * 'customerName'` is a field comparison, not copy. Keeping the keys here means the
 * page carries no unregistered literal while the branch stays explicit.
 */
const COLUMN = {
  state: 'state',
  operationType: 'operationType',
  customerName: 'customerName',
  releaseDefinitionName: 'releaseDefinitionName',
  revision: 'revision',
  createdAt: 'createdAt',
  operationId: 'operationId',
} as const;

const columns = [
  { key: COLUMN.state, label: t('operationCenter.column.state') },
  { key: COLUMN.operationType, label: t('operationCenter.column.type') },
  { key: COLUMN.customerName, label: t('operationCenter.column.customer') },
  { key: COLUMN.releaseDefinitionName, label: t('operationCenter.column.release') },
  { key: COLUMN.revision, label: t('operationCenter.column.revision'), sortable: true, align: 'end' as const },
  { key: COLUMN.createdAt, label: t('operationCenter.column.createdAt'), sortable: true },
  { key: COLUMN.operationId, label: t('operationCenter.column.operation') },
];

function formatTimestamp(value: string | null): string {
  return value
    ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
    : '—';
}

/** The inline name is the point of the aggregate; fall back to the id when it is empty. */
function customerOf(row: Record<string, unknown>): string {
  return String(row.customerName ?? '') || String(row.customerId ?? '');
}

function releaseOf(row: Record<string, unknown>): string {
  return String(row.releaseDefinitionName ?? '') || String(row.releaseDefinitionId ?? '');
}

function detailRoute(row: Record<string, unknown>) {
  return {
    name: 'OperationCenterDetail',
    params: { operationId: String(row.operationId ?? '') },
    query: { releaseName: String(row.releaseDefinitionName ?? '') },
  };
}

async function reload(): Promise<void> {
  await load(cursors.value[cursorIndex.value] ?? '');
}

async function goPrev(): Promise<void> {
  if (!hasPrev.value || loading.value) return;
  cursorIndex.value -= 1;
  await reload();
}

async function goNext(): Promise<void> {
  const token = nextPageToken.value;
  if (!token || loading.value) return;
  cursorIndex.value += 1;
  cursors.value[cursorIndex.value] = token;
  await load(token);
}

onMounted(() => {
  void load();
});
</script>

<template>
  <section class="operation-center">
    <header class="operation-center__header">
      <div>
        <p class="operation-center__eyebrow">{{ t('operationCenter.eyebrow') }}</p>
        <h1>{{ t('operationCenter.title') }}</h1>
        <p class="operation-center__description">{{ t('operationCenter.description') }}</p>
      </div>
    </header>

    <!-- Maintenance is a planned refusal, not a failure: say so instead of showing a
         retryable error, an empty queue, or a spinner that never resolves. -->
    <section
      v-if="maintenance"
      class="operation-center__maintenance"
      role="status"
      data-testid="operation-center-maintenance"
    >
      <p>{{ t('operationCenter.maintenance') }}</p>
    </section>

    <ForbiddenState v-else-if="forbidden" :message="t('operationCenter.forbidden')" />

    <DataTable
      v-else
      :columns="columns"
      :rows="items"
      :row-key="COLUMN.operationId"
      :loading="loading"
      :error="error"
      :empty="t('operationCenter.emptyMessage')"
      @retry="reload"
    >
      <template #cell="{ row, column }">
        <span
          v-if="column.key === COLUMN.state"
          class="operation-center__state"
          :class="`operation-center__state--${String(row.state)}`"
        >
          {{ statusLabel('operation', String(row.state)) }}
        </span>
        <span v-else-if="column.key === COLUMN.operationType" class="operation-center__type">
          {{ statusLabel('operationType', String(row.operationType)) }}
          <span v-if="row.emergency === true" class="operation-center__emergency">
            {{ t('operationCenter.emergency') }}
          </span>
        </span>
        <template v-else-if="column.key === COLUMN.customerName">{{ customerOf(row) }}</template>
        <template v-else-if="column.key === COLUMN.releaseDefinitionName">{{ releaseOf(row) }}</template>
        <span v-else-if="column.key === COLUMN.createdAt">{{ formatTimestamp(String(row.createdAt ?? '')) }}</span>
        <RouterLink v-else-if="column.key === COLUMN.operationId" :to="detailRoute(row)">
          <code>{{ String(row.operationId ?? '') }}</code>
        </RouterLink>
        <template v-else>{{ String(row[column.key] ?? '') }}</template>
      </template>
    </DataTable>

    <nav v-if="showPager" class="operation-center__pager" :aria-label="t('operationCenter.pagination')">
      <button type="button" :disabled="!hasPrev || loading" @click="goPrev">
        {{ t('operationCenter.previous') }}
      </button>
      <p class="operation-center__page" role="status">{{ t('operationCenter.page', { page: pageNumber }) }}</p>
      <button type="button" :disabled="!hasNext || loading" @click="goNext">
        {{ t('operationCenter.next') }}
      </button>
    </nav>
  </section>
</template>

<style scoped>
.operation-center {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-5);
}

.operation-center__header h1 {
  margin: 0;
  font-size: var(--font-size-xl);
}

.operation-center__eyebrow {
  margin: 0;
  color: var(--color-primary);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.operation-center__description {
  margin: var(--space-1) 0 0;
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}

.operation-center__maintenance {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  align-items: center;
  justify-content: space-between;
  padding: var(--space-4);
  border: 1px solid var(--color-warning-border);
  border-radius: var(--radius-lg);
  background: var(--color-warning-soft);
  color: var(--color-warning-ink);
}

.operation-center__maintenance p {
  margin: 0;
}

.operation-center__state {
  padding: 0.1rem var(--space-2);
  border-radius: var(--radius-xl);
  background: var(--color-surface-muted);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
}

.operation-center__state--running,
.operation-center__state--cancelling {
  background: var(--color-info-subtle);
  color: var(--color-info-ink-strong);
}

.operation-center__type {
  display: inline-flex;
  gap: var(--space-2);
  align-items: center;
}

.operation-center__emergency {
  padding: 0.1rem var(--space-2);
  border-radius: var(--radius-xl);
  background: var(--color-danger-surface);
  color: var(--color-error-strong);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
}

.operation-center__pager {
  display: flex;
  gap: var(--space-3);
  align-items: center;
}

.operation-center__pager button {
  padding: var(--space-2) var(--space-4);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  font: inherit;
  cursor: pointer;
}

.operation-center__pager button:disabled {
  color: var(--color-subtle);
  cursor: not-allowed;
}

.operation-center__page {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}
</style>
