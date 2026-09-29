<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onActivated, onBeforeUnmount, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import AuthorizationStaleNotice from '@/components/common/AuthorizationStaleNotice.vue';
import ReleaseInventorySkeleton from '@/components/releases/ReleaseInventorySkeleton.vue';
import ReleaseInventoryTable from '@/components/releases/ReleaseInventoryTable.vue';
import RollbackReleaseDialog from '@/components/releases/RollbackReleaseDialog.vue';
import { useAuthStore } from '@/stores/auth';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import { useScopeNames } from '@/composables/useScopeNames';
import { useReleaseInventoryStore, type StatusFilter, type ReleaseSummary } from '@/stores/releaseInventory';

const route = useRoute();
const router = useRouter();
const auth = useAuthStore();
const inventory = useReleaseInventoryStore();
const authorization = useEmergencyAuthorizationStore();
const cacheMaxAgeMs = 5 * 60 * 1000;
let searchTimer: ReturnType<typeof setTimeout> | undefined;

const customerId = computed(() => String(route.params.customerId ?? ''));
const clusterId = computed(() => String(route.params.clusterId ?? ''));
// Plan N7: a direct URL has no query names, and the old fallback printed raw UUIDs.
const { customerName, clusterName } = useScopeNames();
const canSync = computed(() => auth.user?.roles.some((role) => ['platform_admin', 'release_admin', 'deployer'].includes(role)) === true);
const operationsEnabled = import.meta.env.VITE_ENABLE_RELEASE_OPERATIONS !== 'false';
// AC-033-10: a stale authorization snapshot closes the NEW operation entry. The
// inventory, existing operations and late results stay readable — only the write
// entry is gated, and the server still rejects a call that ignores it.
const writeBlocked = computed(() => !authorization.writeAllowed);
const canCreateOperation = computed(() => operationsEnabled && auth.canCreateReleaseOperation && !writeBlocked.value);
// A11: rollback needs the same write gate as creating an operation (the server
// additionally requires deployer/administrator capability).
const canRollback = computed(() => canCreateOperation.value);
const rollbackTarget = ref<ReleaseSummary | null>(null);

function openRollback(release: ReleaseSummary): void {
  rollbackTarget.value = release;
}

async function handleRollbackCreated(result: { operationId: string; fromRevision: number; toRevision: number }): Promise<void> {
  const release = rollbackTarget.value;
  rollbackTarget.value = null;
  if (!release?.releaseDefinitionId) return;
  // The rollback is a NEW operation; land the operator on its timeline.
  await router.push({
    name: 'OperationDetail',
    params: { customerId: customerId.value, clusterId: clusterId.value, releaseId: release.releaseDefinitionId, operationId: result.operationId },
    query: { customerName: customerName.value, clusterName: clusterName.value, releaseName: release.name },
  });
}

// One scoped Authorization Snapshot per page — the emergency/convergence
// entry columns derive from it; no per-row authorization RPC (AC-058-04/08).
watch(
  [customerId],
  ([nextCustomerId]) => {
    const organizationId = auth.activeOrganization?.id ?? '';
    if (organizationId && nextCustomerId) {
      void authorization.load(organizationId, nextCustomerId);
    } else {
      authorization.reset();
    }
  },
  { immediate: true },
);

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
  authorization.reset();
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
    <nav class="breadcrumbs" :aria-label="t('common.breadcrumb')">
      <span>{{ customerName }}</span><span aria-hidden="true">/</span>
      <span>{{ clusterName }}</span><span aria-hidden="true">/</span>
      <strong>{{ t('release.inventory.breadcrumb') }}</strong>
    </nav>

    <header class="inventory-page__header">
      <div>
        <p class="eyebrow">{{ t('release.inventory.eyebrow') }}</p>
        <h1>{{ clusterName }} {{ t('release.inventory.breadcrumb') }}</h1>
        <p>展示 operator 最近同步的 Helm Release；共 {{ inventory.totalCount }} 条。</p>
      </div>
      <div class="inventory-actions">
        <button type="button" :disabled="inventory.loading" @click="inventory.refresh">刷新</button>
        <button v-if="canSync" type="button" class="primary" :disabled="inventory.syncing" @click="inventory.triggerSync">
          {{ inventory.syncing ? '同步中…' : '触发同步' }}
        </button>
      </div>
    </header>

    <AuthorizationStaleNotice :stale="writeBlocked" />

    <div v-if="inventory.syncError" class="notice notice--warning" role="alert">{{ inventory.syncError }}</div>
    <div v-else-if="inventory.syncRequestId" class="notice" role="status">
      同步请求已创建：<code>{{ inventory.syncRequestId }}</code>。完成后请手动刷新。
    </div>

    <div class="filters">
      <label>
        状态
        <select :value="inventory.statusFilter ?? ''" @change="handleStatusChange">
          <option value="">{{ t('release.filter.allStatus') }}</option>
          <option value="active">{{ t('release.status.active') }}</option>
          <option value="missing">{{ t('release.status.missing') }}</option>
          <option value="out_of_sync">{{ t('release.status.outOfSync') }}</option>
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
      <ReleaseInventoryTable
        v-else
        :releases="inventory.releases"
        :can-create-operation="canCreateOperation"
        :can-emergency="authorization.canExecuteEmergency"
        :can-convergence="authorization.canCreateValuesRevision"
        :can-rollback="canRollback"
        :customer-id="customerId"
        :cluster-id="clusterId"
        :customer-name="customerName"
        :cluster-name="clusterName"
        @rollback="openRollback"
      />
      <RollbackReleaseDialog
        v-if="rollbackTarget"
        :open="true"
        :release-definition-id="rollbackTarget.releaseDefinitionId"
        :release-name="`${rollbackTarget.namespace}/${rollbackTarget.name}`"
        :current-revision="rollbackTarget.revision"
        @close="rollbackTarget = null"
        @created="handleRollbackCreated"
      />
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
.breadcrumbs { display: flex; flex-wrap: wrap; gap: 0.45rem; color: var(--color-muted); font-size: var(--font-size-sm); }
.inventory-page__header { display: flex; align-items: flex-start; justify-content: space-between; gap: 1.5rem; }
h1, p { margin: 0; }
.inventory-page__header div:first-child { display: grid; gap: 0.35rem; }
.eyebrow { color: var(--color-primary); font-size: var(--font-size-xs); font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.inventory-actions, .load-more { display: flex; gap: 0.75rem; }
button, select, input { min-height: 2.5rem; padding: 0.45rem 0.75rem; border: 1px solid var(--color-border-strong); border-radius: 0.45rem; background: var(--color-surface); }
button { cursor: pointer; }
button:disabled { cursor: not-allowed; opacity: 0.6; }
button.primary { border-color: var(--color-primary); background: var(--color-primary); color: var(--color-on-accent); }
.filters { display: grid; grid-template-columns: minmax(10rem, 14rem) minmax(18rem, 1fr); gap: 1rem; padding: 1rem; border: 1px solid var(--color-border); border-radius: 0.65rem; background: var(--color-surface); }
.filters label { display: grid; gap: 0.35rem; color: var(--color-muted-strong); font-size: var(--font-size-sm); font-weight: 700; }
.notice { display: flex; align-items: center; gap: 0.75rem; padding: 0.8rem 1rem; border: 1px solid var(--color-info-border); border-radius: 0.5rem; background: var(--color-info-soft); color: var(--color-info-ink); }
.notice--warning { border-color: var(--color-warning-border); background: var(--color-warning-soft); color: var(--color-warning-ink); }
.load-more { justify-content: center; }
@media (max-width: 48rem) {
  .inventory-page__header { flex-direction: column; }
  .filters { grid-template-columns: 1fr; }
}
</style>
