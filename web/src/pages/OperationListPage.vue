<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onMounted, ref } from 'vue';
import { statusLabel } from '@/i18n/status-labels';
import { useRoute } from 'vue-router';
import type { RouteLocationRaw } from 'vue-router';
import CopyableHash from '@/components/common/CopyableHash.vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import Toast from '@/components/common/Toast.vue';
import { listOperations, mapOperationError, type OperationSummaryItem } from '@/connect/operation-api';
import type {} from '@/types/operation';
import { formatTimestamp } from '@/utils/formatTimestamp';

/*
 * Operation history for one release definition (W3 / UX plan N5).
 *
 * Before this page the only way to a release operation was the detail route you
 * had just been redirected to: there was no list, so a user who navigated away
 * could not get back, and an operator could not see what had run against a
 * release. `ListOperations` (REQ-056) already existed server-side.
 */
const route = useRoute();
const customerId = computed(() => String(route.params.customerId ?? ''));
const clusterId = computed(() => String(route.params.clusterId ?? ''));
const releaseId = computed(() => String(route.params.releaseId ?? ''));
const customerName = computed(() => String(route.query.customerName ?? customerId.value));
const clusterName = computed(() => String(route.query.clusterName ?? clusterId.value));
const releaseName = computed(() => String(route.query.releaseName ?? ''));

const STATUS_OPTIONS: { value: string; label: string }[] = [
  { value: '', label: '全部状态' },
  { value: 'running', label: '进行中' },
  { value: 'succeeded', label: '已成功' },
  { value: 'failed', label: '已失败' },
  { value: 'cancelled', label: '已取消' },
];

const rows = ref<OperationSummaryItem[]>([]);
const nextCursor = ref('');
const statusFilter = ref('');
const loading = ref(false);
const loadingMore = ref(false);
const error = ref<string | null>(null);
const cursorReset = ref(false);

// Loading/empty/error are mutually exclusive branches of ONE chain, so a failed
// request can never also read as "no operations" (the UX-010 class of defect).
const showError = computed(() => error.value !== null && rows.value.length === 0);
const showEmpty = computed(() => !loading.value && error.value === null && rows.value.length === 0);
const showTable = computed(() => rows.value.length > 0);

async function load(append = false, clearOnError = false): Promise<void> {
  if (append) loadingMore.value = true;
  else loading.value = true;
  error.value = null;
  if (!append) cursorReset.value = false;
  try {
    const page = await listOperations(releaseId.value, {
      statusFilter: statusFilter.value,
      cursor: append ? nextCursor.value : '',
    });
    rows.value = append ? [...rows.value, ...page.operations] : page.operations;
    nextCursor.value = page.nextCursor;
  } catch (requestError) {
    const mapped = mapOperationError(requestError);
    if (mapped.code === 'invalid_cursor') {
      // Contract (connect-surface §分页): an expired cursor resets THAT list and
      // keeps the form/selection, so re-query from the first page with the same
      // filter instead of showing a dead end.
      error.value = null;
      nextCursor.value = '';
      if (append) {
        // Re-query first: load() clears the notice flag when it starts, so set it
        // afterwards to report what happened.
        await load(false);
        cursorReset.value = true;
        return;
      }
      cursorReset.value = true;
    }
    // Keep whatever is already on screen: a refresh failure must not read as
    // "this release never ran anything". The template shows the error state only
    // when there is nothing loaded, and an inline warning otherwise.
    error.value = mapped.message;
    // …unless the rows belong to a DIFFERENT filter: keeping them under a failed
    // filter query would show results that do not match the form.
    if (clearOnError) rows.value = [];
  } finally {
    loading.value = false;
    loadingMore.value = false;
  }
}

/**
 * Refresh keeps the rows on failure (UX-010: a failed refresh must not read as
 * "nothing ran"); a FILTER change is a different query, so its failure clears the
 * previous filter's rows instead of showing results that do not match the form.
 */
async function refresh(): Promise<void> {
  await load(false);
}

async function applyFilter(): Promise<void> {
  await load(false, true);
}

function operationRoute(row: OperationSummaryItem) {
  return {
    name: 'OperationDetail',
    params: { customerId: customerId.value, clusterId: clusterId.value, releaseId: releaseId.value, operationId: row.operationId },
    query: { customerName: customerName.value, clusterName: clusterName.value, releaseName: releaseName.value },
  };
}

const inventoryRoute = computed(() => ({
  name: 'ReleaseInventory',
  params: { customerId: customerId.value, clusterId: clusterId.value },
  query: { customerName: customerName.value, clusterName: clusterName.value },
}));

const createRoute = computed(() => ({
  name: 'OperationCreate',
  params: { customerId: customerId.value, clusterId: clusterId.value, releaseId: releaseId.value },
  query: { customerName: customerName.value, clusterName: clusterName.value, releaseName: releaseName.value },
}));

/*
 * TASK-394 (consistency wave 5): the hand-rolled `<nav aria-label="面包屑">` became
 * PageHeader's `crumbs`. The trail keeps the old navigation graph (customer list →
 * clusters → release inventory → this page) and stays a `computed`, so a route/query change
 * re-derives both the labels and the link targets at render time.
 *
 * Labels are NAMES only. `customerName` falls back to the route id when a direct URL carries
 * no query name (in-app links always carry it), and a crumb whose label would be that id is
 * DROPPED rather than printed — PageHeader's contract forbids a trail built from params, and
 * this page is only reached with a customer/cluster in the URL. The release level has the
 * same rule one level down: without `releaseName` the definition is identified by the
 * CopyableHash in the header actions, never by the raw id in the trail.
 */
const crumbs = computed<Array<{ label: string; to?: RouteLocationRaw }>>(() => {
  const trail: Array<{ label: string; to?: RouteLocationRaw }> = [
    { label: t('nav.customers'), to: { name: 'CustomerList' } },
  ];
  if (customerName.value && customerName.value !== customerId.value) {
    trail.push({
      label: customerName.value,
      to: { name: 'ClusterList', params: { customerId: customerId.value } },
    });
  }
  if (clusterName.value && clusterName.value !== clusterId.value) {
    trail.push({ label: clusterName.value, to: inventoryRoute.value });
  }
  // The last crumb is the current page; PageHeader renders it as aria-current, not a link.
  trail.push({ label: t('operation.list.eyebrow') });
  return trail;
});

/* The release NAME is the subtitle; a direct URL has none, so the subtitle is omitted and
   the definition id surfaces as a truncated CopyableHash in the header actions instead. */
const subtitle = computed(() => releaseName.value);

onMounted(() => load(false));
</script>

<template>
  <section class="operation-list">
    <PageHeader
      :title="t('operation.list.eyebrow')"
      :eyebrow="t('operation.list.subtitle')"
      :subtitle="subtitle"
      :crumbs="crumbs"
    >
      <template #actions>
        <div class="operation-list__actions">
          <!--
            Identity fallback (TASK-394): without a `releaseName` the definition would
            otherwise be unnamed on screen. CopyableHash keeps the id readable and copyable
            while the full value lives in `title`; the raw UUID is never printed as text.
          -->
          <span v-if="!releaseName && releaseId" class="operation-list__definition">
            <span class="operation-list__definition-label">{{ t('operation.list.definition') }}</span>
            <CopyableHash :value="releaseId" />
          </span>
          <RouterLink :to="inventoryRoute">{{ t('operation.list.back') }}</RouterLink>
          <RouterLink class="primary" :to="createRoute">{{ t('operation.list.create') }}</RouterLink>
        </div>
      </template>
    </PageHeader>

    <form class="operation-list__filter" @submit.prevent="refresh">
      <label>
        状态
        <select v-model="statusFilter" name="status" @change="applyFilter">
          <option v-for="option in STATUS_OPTIONS" :key="option.value" :value="option.value">{{ option.label }}</option>
        </select>
      </label>
      <button type="submit" :disabled="loading">{{ loading ? '查询中…' : '刷新' }}</button>
    </form>

    <!-- The reset notice belongs to the whole page, not to the table branch: it
         must still be visible when the re-query comes back empty. -->
    <!-- TASK-267: the stale-cursor recovery report uses the shared Toast primitive. -->
    <Toast
      v-if="cursorReset"
      class="operation-list__notice"
      testid="operation-list-cursor-reset"
      @dismiss="cursorReset = false"
    >
      页码已过期，已从最新一页重新加载（筛选条件保留）。
    </Toast>
    <ErrorState
      v-if="showError"
      title="操作历史加载失败"
      :message="error ?? ''"
      action-label="重试"
      @action="load(false)"
    />
    <LoadingState v-else-if="loading && !showTable" message="正在加载操作历史…" />
    <EmptyState
      v-else-if="showEmpty"
      title="暂无操作记录"
      :message="statusFilter ? '当前筛选条件下没有操作。' : '该 ReleaseDefinition 还没有执行过操作。'"
    />
    <template v-else>
      <!-- A refresh failure keeps the loaded page and reports inline (UX-010). -->
      <div v-if="error" class="operation-list__warning" role="alert">
        {{ error }}
        <button type="button" @click="load(false)">重试</button>
      </div>
      <div
        class="table-wrap operation-list__table-wrap"
        role="region"
        :aria-label="t('operations.table.scrollRegion')"
        tabindex="0"
      >
        <table class="operation-list__table">
        <thead>
          <tr>
            <th scope="col">类型</th>
            <th scope="col">状态</th>
            <th scope="col">目标 Revision</th>
            <th scope="col">创建时间</th>
            <th scope="col">Operation</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in rows" :key="row.operationId" :data-testid="`operation-row-${row.operationId}`">
            <td>{{ row.operationType }}</td>
            <td>
              <span class="operation-list__state" :class="`operation-list__state--${row.state}`">
                {{ statusLabel('operation', row.state) }}
              </span>
            </td>
            <td>{{ row.revision }}</td>
            <td>{{ formatTimestamp(row.createdAt) }}</td>
            <td>
              <RouterLink :to="operationRoute(row)"><code>{{ row.operationId }}</code></RouterLink>
            </td>
          </tr>
        </tbody>
        </table>
      </div>
      <button
        v-if="nextCursor"
        type="button"
        class="operation-list__more"
        :disabled="loadingMore"
        @click="load(true)"
      >
        {{ loadingMore ? '加载中…' : '加载更多' }}
      </button>
    </template>
  </section>
</template>

<style scoped>
/* Direction-C page root: the shell's `main` already pads 24px (12px at 375). The old
   `padding: var(--space-5)` was a second padding on top of it. */
.operation-list {
  display: grid;
  gap: var(--space-4);
  min-width: 0;
}

/* PageHeader keeps a 20px bottom margin for pages that are not grids; the grid gap is
   the only space between the header and the filter card here. */
.operation-list :deep(.page-header) {
  margin-bottom: 0;
}

.operation-list__actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  align-items: center;
}

.operation-list__definition {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}

.operation-list__definition-label {
  color: var(--mu);
  font-size: var(--font-size-xs);
}

.operation-list__actions .primary {
  padding: var(--space-2) var(--space-4);
  border: 1px solid var(--color-primary);
  border-radius: var(--radius-md);
  background: var(--color-primary);
  color: var(--color-on-accent);
  font-weight: var(--font-weight-medium);
  text-decoration: none;
}

/* Direction-C filter card. It used to be a bare form row, which read as part of the page
   rather than as a control group; the strong border is the one that is visible in dark. */
.operation-list__filter {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  align-items: flex-end;
  padding: var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
}

.operation-list__filter label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.operation-list__filter select,
.operation-list__filter button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  font: inherit;
}

.operation-list__notice {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-info-border-soft);
  border-radius: var(--radius-lg);
  background: var(--color-info-soft);
  color: var(--color-info-ink);
  font-size: var(--font-size-sm);
}

.operation-list__warning {
  display: flex;
  gap: var(--space-3);
  align-items: center;
  padding: var(--space-3);
  border: 1px solid var(--color-warning-border);
  border-radius: var(--radius-lg);
  background: var(--color-warning-soft);
  color: var(--color-warning-ink);
}

/* Direction-C table frame. `.table-wrap` owns horizontal containment (base.css), so the
   5-column table scrolls inside its own box at 375px instead of widening the page. */
.operation-list__table-wrap {
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
}

.operation-list__table {
  width: 100%;
  min-width: 44rem;
  border-collapse: collapse;
  background: var(--sf);
}

/* The header used to share one th/td rule, so it had no band and only the UA bold weight
   separated it from the rows. Muted band + readable ink is the direction-C table header. */
.operation-list__table th {
  padding: 10px 20px;
  background: var(--color-surface-muted);
  color: var(--color-muted-strong);
  font-size: var(--font-size-xs);
  font-weight: 600;
  text-align: left;
  /* Without nowrap the header breaks one glyph per line at 375px; the frame scrolls. */
  white-space: nowrap;
}

.operation-list__table td {
  padding: 10px 20px;
  border-top: 1px solid var(--color-border-strong);
  text-align: left;
}

.operation-list__state {
  padding: 0.1rem var(--space-2);
  border-radius: var(--radius-xl);
  background: var(--color-surface-muted);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
  /* The label is a two-character CJK word; without nowrap a narrow column breaks it one
     glyph per line. The table scrolls inside its frame instead. */
  white-space: nowrap;
}

.operation-list__state--succeeded {
  background: var(--color-success-surface);
  color: var(--color-success-ink);
}

.operation-list__state--failed,
.operation-list__state--timeout {
  background: var(--color-danger-surface);
  color: var(--color-error-strong);
}

.operation-list__state--running,
.operation-list__state--cancelling {
  background: var(--color-info-subtle);
  color: var(--color-info-ink-strong);
}

.operation-list__more {
  justify-self: start;
  padding: var(--space-2) var(--space-4);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}
</style>
