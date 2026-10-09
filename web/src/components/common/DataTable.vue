<script setup lang="ts">
import { computed, ref } from 'vue';
import EmptyState from './EmptyState.vue';
import ErrorState from './ErrorState.vue';
import LoadingState from './LoadingState.vue';
import { t } from '@/i18n/messages';

/*
 * The one table primitive (ux-revamp-plan §9.2, ADR-029 clause 3): five tables were
 * hand-rolled, each with its own empty/error/loading markup and none of them
 * sortable. Two things are the contract here:
 *
 *  - sorting is owned by this component. A sortable header renders a NATIVE
 *    `<button>`, so Tab reaches it and Enter/Space activate it with no extra JS
 *    (a keydown handler on top of a button would toggle twice in a real browser),
 *    and the active column publishes `aria-sort` — the only signal a screen reader
 *    gets for the sort state;
 *  - the three async states are the shared state components, not local variants,
 *    and they take precedence in a fixed order: error → loading → empty → rows.
 */
interface DataTableColumn {
  key: string;
  label: string;
  sortable?: boolean;
  align?: 'start' | 'center' | 'end';
}

interface DataTableError {
  message: string;
  details?: string;
  /** Renders ErrorState's retry action. The owner handles the `retry` event. */
  retryable?: boolean;
}

type DataTableRow = Record<string, unknown>;

interface Props {
  columns: DataTableColumn[];
  rows: DataTableRow[];
  loading?: boolean;
  /** Message for the empty state; a `#empty` slot replaces the whole state. */
  empty?: string;
  error?: DataTableError | null;
  /**
   * Field used as the row key. Falling back to the index keeps sorting correct but
   * lets a stateful cell be reused across rows, so rows with an id should pass it.
   */
  rowKey?: string;
}

const props = withDefaults(defineProps<Props>(), {
  loading: false,
  empty: '',
  error: null,
  rowKey: undefined,
});

const emit = defineEmits<{
  sort: [sort: { key: string; direction: 'asc' | 'desc' }];
  retry: [];
}>();

defineSlots<{
  /** Replaces the whole empty state (a page-specific illustration or action). */
  empty?: () => unknown;
  /** Per-cell override for anything richer than text (badges, links, actions). */
  cell?: (props: { row: DataTableRow; column: DataTableColumn; value: unknown }) => unknown;
}>();

const sortKey = ref('');
const sortDirection = ref<'asc' | 'desc'>('asc');

/** `undefined` for a non-sortable column: no aria-sort beats a wrong aria-sort. */
function ariaSort(column: DataTableColumn): 'ascending' | 'descending' | 'none' | undefined {
  if (!column.sortable) return undefined;
  if (sortKey.value !== column.key) return 'none';
  return sortDirection.value === 'asc' ? 'ascending' : 'descending';
}

function toggleSort(column: DataTableColumn): void {
  if (!column.sortable) return;
  if (sortKey.value === column.key) {
    sortDirection.value = sortDirection.value === 'asc' ? 'desc' : 'asc';
  } else {
    sortKey.value = column.key;
    sortDirection.value = 'asc';
  }
  emit('sort', { key: sortKey.value, direction: sortDirection.value });
}

function compareValues(left: unknown, right: unknown): number {
  if (left === right) return 0;
  // Missing values stay last in the ascending order, so a "never" timestamp cannot
  // displace real data at one end of the column.
  if (left === null || left === undefined) return 1;
  if (right === null || right === undefined) return -1;
  if (typeof left === 'number' && typeof right === 'number') return left - right;
  return String(left).localeCompare(String(right), undefined, { numeric: true, sensitivity: 'base' });
}

const sortedRows = computed(() => {
  if (!sortKey.value) return props.rows;
  const key = sortKey.value;
  const factor = sortDirection.value === 'asc' ? 1 : -1;
  return [...props.rows].sort((left, right) => factor * compareValues(left[key], right[key]));
});

function cellText(value: unknown): string {
  return value === null || value === undefined ? '' : String(value);
}
</script>

<template>
  <div class="data-table">
    <ErrorState
      v-if="error"
      :message="error.message"
      :details="error.details"
      :action-label="error.retryable ? t('action.retry') : ''"
      @action="emit('retry')"
    />

    <LoadingState v-else-if="loading" />

    <slot v-else-if="rows.length === 0" name="empty">
      <EmptyState :message="empty" />
    </slot>

    <table v-else class="data-table__table">
      <thead>
        <tr>
          <th
            v-for="column in columns"
            :key="column.key"
            scope="col"
            :data-key="column.key"
            :class="`data-table__cell--${column.align ?? 'start'}`"
            :aria-sort="ariaSort(column)"
          >
            <button
              v-if="column.sortable"
              type="button"
              class="data-table__sort"
              @click="toggleSort(column)"
            >
              {{ column.label }}
            </button>
            <template v-else>{{ column.label }}</template>
          </th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(row, index) in sortedRows" :key="rowKey ? String(row[rowKey]) : index">
          <td
            v-for="column in columns"
            :key="column.key"
            :class="`data-table__cell--${column.align ?? 'start'}`"
          >
            <slot name="cell" :row="row" :column="column" :value="row[column.key]">
              {{ cellText(row[column.key]) }}
            </slot>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.data-table__table {
  width: 100%;
  border-collapse: collapse;
}

.data-table__table th,
.data-table__table td {
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border);
}

.data-table__table th {
  background: var(--color-surface-muted);
  color: var(--color-muted-strong);
  font-size: var(--font-size-md);
  font-weight: var(--font-weight-medium);
}

/*
 * Column alignment is carried by these classes, not by the base rule above: the UA
 * default for <th> is centered, so every cell needs an explicit start alignment and
 * the center/end variant must be able to win against it.
 */
.data-table__cell--start {
  text-align: start;
}

.data-table__cell--center {
  text-align: center;
}

.data-table__cell--end {
  text-align: end;
}

.data-table__sort {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  padding: 0;
  border: 0;
  background: none;
  color: inherit;
  font: inherit;
  font-weight: inherit;
  cursor: pointer;
}

/* Sort affordance: without it a sortable header is indistinguishable from a label,
   and the arrow is the only hint a mouse user gets. Decorative — the state itself
   travels through aria-sort. */
.data-table__sort::after {
  color: var(--color-subtle);
  content: '↕';
  font-size: var(--font-size-xs);
}

th[aria-sort='ascending'] .data-table__sort::after {
  color: var(--color-text);
  content: '↑';
}

th[aria-sort='descending'] .data-table__sort::after {
  color: var(--color-text);
  content: '↓';
}
</style>
