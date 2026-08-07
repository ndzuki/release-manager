<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, watch } from 'vue';
import { useRoute } from 'vue-router';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import ReleaseInventorySkeleton from '@/components/releases/ReleaseInventorySkeleton.vue';
import ReleaseInventoryTable from '@/components/releases/ReleaseInventoryTable.vue';
import { useAuthStore } from '@/stores/auth';
import { useReleaseInventoryStore, type StatusFilter } from '@/stores/releaseInventory';

const route = useRoute();
const auth = useAuthStore();
const inventory = useReleaseInventoryStore();
const cacheMaxAgeMs = 5 * 60 * 1000;
let searchTimer: ReturnType<typeof setTimeout> | undefined;

const customerId = computed(() => String(route.params.customerId ?? ''));
const clusterId = computed(() => String(route.params.clusterId ?? ''));
const customerName = computed(() => String(route.query.customerName ?? customerId.value));
const clusterName = computed(() => String(route.query.clusterName ?? clusterId.value));
const canSync = computed(() => auth.user?.roles.some((role) => ['platform_admin', 'release_admin', 'deployer'].includes(role)) === true);

watch(
  [customerId, clusterId],
  async ([nextCustomerId, nextClusterId]) => {
    inventory.setScope(nextCustomerId, nextClusterId);
    await inventory.load();
  },
  { immediate: true },
);

onActivated(() => {
  if (inventory.lastLoadedAt !== null && Date.now() - inventory.lastLoadedAt > cacheMaxAgeMs) {
    void inventory.refresh();
  }
});

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer);
});

async function handleStatusChange(event: Event): Promise<void> {
  const value = (event.target as HTMLSelectElement).value as StatusFilter | '';
  inventory.setStatusFilter(value === '' ? undefined : value);
  await inventory.refresh();
}

function handleSearchInput(event: Event): void {
  const value = (event.target as HTMLInputElement).value;
  if (!inventory.setNameSearch(value)) return;
  if (searchTimer) clearTimeout(searchTimer);
  searchTimer = setTimeout(() => void inventory.refresh(), 300);
}
</script>

<template>
  <section class="inventory-page">
    <nav class="breadcrumbs" aria-label="Breadcrumb">
      <span>{{ customerName }}</span><span aria-hidden="true">/</span>
      <span>{{ clusterName }}</span><span aria-hidden="true">/</span>
      <strong>Releases</strong>
    </nav>

    <header class="inventory-page__header">
      <div>
        <p class="eyebrow">Release inventory</p>
        <h1>{{ clusterName }} Releases</h1>
        <p>展示 operator 最近同步的 Helm Release；共 {{ inventory.totalCount }} 条。</p>
      </div>
      <div class="inventory-actions">
        <button type="button" :disabled="inventory.loading" @click="inventory.refresh">刷新</button>
        <button v-if="canSync" type="button" class="primary" :disabled="inventory.syncing" @click="inventory.triggerSync">
          {{ inventory.syncing ? '同步中…' : '触发同步' }}
        </button>
      </div>
    </header>

    <div v-if="inventory.syncError" class="notice notice--warning" role="alert">{{ inventory.syncError }}</div>
    <div v-else-if="inventory.syncRequestId" class="notice" role="status">
      同步请求已创建：<code>{{ inventory.syncRequestId }}</code>。完成后请手动刷新。
    </div>

    <div class="filters">
      <label>
        状态
        <select :value="inventory.statusFilter ?? ''" @change="handleStatusChange">
          <option value="">全部状态</option>
          <option value="active">Active</option>
          <option value="missing">Missing</option>
          <option value="out_of_sync">Out of sync</option>
        </select>
      </label>
      <label class="filters__search">
        搜索 Release
        <input :value="inventory.nameSearch" maxlength="253" placeholder="按 release name 搜索" @input="handleSearchInput" />
      </label>
    </div>

    <ReleaseInventorySkeleton v-if="inventory.loading && inventory.releases.length === 0" />
    <ErrorState
      v-else-if="inventory.error && inventory.releases.length === 0"
      title="Release 列表加载失败"
      :message="inventory.error"
      action-label="重试"
      @action="inventory.refresh"
    />
    <template v-else>
      <div v-if="inventory.error" class="notice notice--warning" role="alert">
        {{ inventory.error }}
        <button type="button" @click="inventory.refresh">重试</button>
      </div>
      <EmptyState
        v-if="inventory.isEmpty"
        title="暂无 Release"
        message="operator 同步后将自动出现"
        action-label="刷新"
        @action="inventory.refresh"
      />
      <ReleaseInventoryTable v-else :releases="inventory.releases" :customer-id="customerId" :cluster-id="clusterId" />
      <div v-if="inventory.hasMore" class="load-more">
        <button type="button" :disabled="inventory.appending" @click="inventory.load({ append: true })">
          {{ inventory.appending ? '加载中…' : '加载更多' }}
        </button>
      </div>
    </template>
  </section>
</template>

<style scoped>
.inventory-page { display: grid; gap: 1.5rem; }
.breadcrumbs { display: flex; flex-wrap: wrap; gap: 0.45rem; color: #64748b; font-size: 0.85rem; }
.inventory-page__header { display: flex; align-items: flex-start; justify-content: space-between; gap: 1.5rem; }
h1, p { margin: 0; }
.inventory-page__header div:first-child { display: grid; gap: 0.35rem; }
.eyebrow { color: #2563eb; font-size: 0.75rem; font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.inventory-actions, .load-more { display: flex; gap: 0.75rem; }
button, select, input { min-height: 2.5rem; padding: 0.45rem 0.75rem; border: 1px solid #cbd5e1; border-radius: 0.45rem; background: #fff; }
button { cursor: pointer; }
button:disabled { cursor: not-allowed; opacity: 0.6; }
button.primary { border-color: #2563eb; background: #2563eb; color: #fff; }
.filters { display: grid; grid-template-columns: minmax(10rem, 14rem) minmax(18rem, 1fr); gap: 1rem; padding: 1rem; border: 1px solid #e2e8f0; border-radius: 0.65rem; background: #fff; }
.filters label { display: grid; gap: 0.35rem; color: #475569; font-size: 0.8rem; font-weight: 700; }
.notice { display: flex; align-items: center; gap: 0.75rem; padding: 0.8rem 1rem; border: 1px solid #93c5fd; border-radius: 0.5rem; background: #eff6ff; color: #1e3a8a; }
.notice--warning { border-color: #fdba74; background: #fff7ed; color: #9a3412; }
.load-more { justify-content: center; }
@media (max-width: 48rem) {
  .inventory-page__header { flex-direction: column; }
  .filters { grid-template-columns: 1fr; }
}
</style>
