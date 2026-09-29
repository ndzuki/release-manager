<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onMounted, ref } from 'vue';
import { statusLabel } from '@/i18n/status-labels';
import { useRoute } from 'vue-router';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import { listOperations, mapOperationError, type OperationSummaryItem } from '@/connect/operation-api';
import type {} from '@/types/operation';

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

function formatTimestamp(value: string | null): string {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : '—';
}

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

onMounted(() => load(false));
</script>

<template>
  <section class="operation-list">
    <nav class="operation-list__crumbs" aria-label="面包屑">
      <RouterLink :to="{ name: 'CustomerList' }">{{ t('nav.customers') }}</RouterLink>
      <span aria-hidden="true">/</span>
      <RouterLink :to="{ name: 'ClusterList', params: { customerId } }">{{ customerName }}</RouterLink>
      <span aria-hidden="true">/</span>
      <RouterLink :to="inventoryRoute">{{ clusterName }}</RouterLink>
      <span aria-hidden="true">/</span>
      <span>{{ t('operation.list.eyebrow') }}</span>
    </nav>

    <header class="operation-list__header">
      <div>
        <p class="eyebrow">{{ t('operation.list.subtitle') }}</p>
        <h1>操作历史</h1>
        <p class="operation-list__subtitle">
          {{ releaseName || releaseId }}
          <span v-if="releaseName" class="operation-list__id">{{ releaseId }}</span>
        </p>
      </div>
      <div class="operation-list__actions">
        <RouterLink :to="inventoryRoute">返回发布清单</RouterLink>
        <RouterLink class="primary" :to="createRoute">创建操作</RouterLink>
      </div>
    </header>

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
    <div v-if="cursorReset" class="operation-list__notice" role="status">
      页码已过期，已从最新一页重新加载（筛选条件保留）。
    </div>
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
.operation-list {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-5);
}

.operation-list__crumbs {
  display: flex;
  gap: var(--space-2);
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}

.operation-list__header {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  align-items: flex-start;
  justify-content: space-between;
}

.operation-list__header h1 {
  margin: 0;
  font-size: var(--font-size-xl);
}

.eyebrow {
  margin: 0;
  color: var(--color-primary);
  font-size: var(--font-size-xs);
  font-weight: 800;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.operation-list__subtitle {
  margin: var(--space-1) 0 0;
  color: var(--color-muted);
}

.operation-list__id {
  margin-left: var(--space-2);
  font-family: var(--font-family-mono);
  font-size: var(--font-size-xs);
}

.operation-list__actions {
  display: flex;
  gap: var(--space-3);
  align-items: center;
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

.operation-list__filter {
  display: flex;
  gap: var(--space-3);
  align-items: flex-end;
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
  color: var(--color-info);
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

.operation-list__table {
  width: 100%;
  border-collapse: collapse;
  background: var(--color-surface);
}

.operation-list__table th,
.operation-list__table td {
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border);
  text-align: left;
}

.operation-list__state {
  padding: 0.1rem var(--space-2);
  border-radius: var(--radius-xl);
  background: var(--color-surface-muted);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
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
