<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onActivated, onBeforeUnmount, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import AuthorizationStaleNotice from '@/components/common/AuthorizationStaleNotice.vue';
import Toast from '@/components/common/Toast.vue';
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

/*
 * Direction-C page header (TASK-394): one title, one subtitle and the crumbs carry the
 * NAMES of the customer and cluster — a crumb built from route params would print a UUID,
 * which is the anti-pattern PageHeader exists to stop. Both crumbs stay plain text, as the
 * old hand-rolled breadcrumb was: this task changes the look, not the navigation graph.
 *
 * The title comes from the catalog's `eyebrow` entry — direction C has no eyebrow line,
 * so that string is the page title now (the key name is historical). The last crumb keeps
 * its own `breadcrumb` entry, which is what the crumb itself renders.
 */
const pageTitle = computed(() => t('release.inventory.eyebrow'));
const crumbs = computed(() => [
  { label: customerName.value },
  { label: clusterName.value },
  { label: t('release.inventory.breadcrumb') },
]);
const subtitle = computed(() => `展示 Operator 最近同步的 Helm Release；共 ${inventory.totalCount} 条。`);

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
    <PageHeader :title="pageTitle" :subtitle="subtitle" :crumbs="crumbs">
      <template #actions>
        <button type="button" class="inventory-button" :disabled="inventory.loading" @click="inventory.refresh()">
          {{ t('action.refresh') }}
        </button>
        <button
          v-if="canSync"
          type="button"
          class="inventory-button inventory-button--primary"
          :disabled="inventory.syncing"
          @click="inventory.triggerSync"
        >
          {{ inventory.syncing ? '同步中…' : '触发同步' }}
        </button>
      </template>
    </PageHeader>

    <AuthorizationStaleNotice :stale="writeBlocked" />

    <div v-if="inventory.syncError" class="notice notice--warning" role="alert">
      <span>{{ inventory.syncError }}</span>
      <details v-if="inventory.syncErrorDetails" class="sync-details">
        <summary>技术细节</summary>
        <pre>{{ inventory.syncErrorDetails }}</pre>
      </details>
    </div>
    <!-- TASK-267: the sync-accepted result uses the shared Toast primitive. -->
    <Toast
      v-else-if="inventory.syncRequestId"
      class="notice"
      testid="inventory-sync-notice"
      @dismiss="inventory.syncRequestId = null"
    >
      同步请求已创建：<code>{{ inventory.syncRequestId }}</code>。完成后请手动刷新。
    </Toast>

    <!-- Filter bar. Native controls keep their UA geometry only if the palette does not
         reach them, so both controls are drawn from tokens explicitly (the deployed
         console measured `appearance:auto` with a white background and black text). -->
    <div class="filters">
      <label class="filters__field">
        状态
        <span class="select-wrap">
          <select :value="inventory.statusFilter ?? ''" @change="handleStatusChange">
            <option value="">{{ t('release.filter.allStatus') }}</option>
            <option value="active">{{ t('release.status.active') }}</option>
            <option value="missing">{{ t('release.status.missing') }}</option>
            <option value="out_of_sync">{{ t('release.status.outOfSync') }}</option>
          </select>
          <span class="select-wrap__caret" aria-hidden="true" />
        </span>
      </label>
      <label class="filters__field filters__search">
        搜索 Release
        <input :value="inventory.nameSearch" maxlength="253" placeholder="按 Release 名称搜索" @input="handleSearchInput" />
      </label>
    </div>

    <!-- The three async states are mutually exclusive: at most one of the skeleton, the
         error state and the empty state is in the tree at a time. -->
    <ReleaseInventorySkeleton v-if="inventory.loading && inventory.releases.length === 0" />
    <ErrorState
      v-else-if="inventory.error && inventory.releases.length === 0"
      title="Release 列表加载失败"
      :message="inventory.error"
      :details="inventory.errorDetails ?? undefined"
      action-label="重试"
      @action="inventory.refresh()"
    />
    <template v-else>
      <!-- A refresh failed but the rows we already had are still readable; that is a
           notice, not the error state (which would hide the data). -->
      <div v-if="inventory.error" class="notice notice--warning" role="alert">
        {{ inventory.error }}
        <button type="button" class="inventory-button" @click="inventory.refresh()">重试</button>
      </div>
      <EmptyState
        v-if="inventory.isEmpty"
        title="暂无 Release"
        message="Operator 同步后将自动出现"
        action-label="刷新"
        @action="inventory.refresh()"
      />
      <template v-else>
        <ReleaseInventoryTable
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
        <div v-if="inventory.hasMore" class="load-more">
          <button type="button" class="inventory-button" :disabled="inventory.appending" @click="inventory.load({ append: true })">
            {{ inventory.appending ? '加载中…' : '加载更多' }}
          </button>
        </div>
      </template>
      <RollbackReleaseDialog
        v-if="rollbackTarget"
        :open="true"
        :release-definition-id="rollbackTarget.releaseDefinitionId"
        :release-name="`${rollbackTarget.namespace}/${rollbackTarget.name}`"
        :current-revision="rollbackTarget.revision"
        @close="rollbackTarget = null"
        @created="handleRollbackCreated"
      />
    </template>
  </section>
</template>

<style scoped>
.inventory-page {
  display: grid;
  gap: var(--space-5);
}

/* PageHeader owns a 20px bottom margin for pages that do not stack their sections in a
   grid; this page does, so the gap is the only space between the header and the notice. */
.inventory-page :deep(.page-header) {
  margin-bottom: 0;
}

.inventory-button {
  padding: 7px 14px;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
  color: var(--color-text);
  font: inherit;
  cursor: pointer;
}

.inventory-button:hover:not(:disabled) {
  background: var(--color-surface-muted);
}

.inventory-button--primary {
  border-color: var(--color-primary);
  background: var(--color-primary);
  color: var(--color-on-accent);
  font-weight: var(--font-weight-medium);
}

.inventory-button--primary:hover {
  background: var(--color-primary-hover);
}

/* Declared after the primary rules so it wins the equal-specificity tie: a disabled
   control resolves to the neutral triple and never to an enabled semantic colour. */
.inventory-button:disabled,
.inventory-button--primary:disabled {
  border-color: var(--color-disabled-border);
  background: var(--color-disabled-bg);
  color: var(--color-disabled-text);
  cursor: not-allowed;
}

.filters {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  align-items: flex-end;
  padding: var(--space-3) var(--space-4);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

.filters__field {
  display: grid;
  flex: none;
  gap: var(--space-1);
  color: var(--color-muted-strong);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-medium);
}

.filters__search {
  flex: 1 1 14rem;
  min-width: 0;
}

.filters select,
.filters input {
  padding: 7px 11px;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-lg);
  background: var(--color-bg);
  color: var(--color-text);
}

.filters input::placeholder {
  color: var(--color-subtle);
}

/* The UA select arrow is painted outside the token palette; the caret is drawn from the
   muted text colour instead, so both themes get the same control. */
.select-wrap {
  position: relative;
  display: block;
}

.select-wrap select {
  width: 100%;
  padding-right: 30px;
  appearance: none;
}

.select-wrap__caret {
  position: absolute;
  top: 50%;
  right: 12px;
  width: 6px;
  height: 6px;
  border-right: 1px solid var(--color-muted);
  border-bottom: 1px solid var(--color-muted);
  transform: translateY(-70%) rotate(45deg);
  pointer-events: none;
}

.notice {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.8rem 1rem;
  border: 1px solid var(--color-info-border);
  border-radius: 0.5rem;
  background: var(--color-info-soft);
  color: var(--color-info-ink);
}

.notice--warning {
  border-color: var(--color-warning-border);
  background: var(--color-warning-soft);
  color: var(--color-warning-ink);
}

.load-more {
  display: flex;
  justify-content: center;
}

@media (max-width: 48rem) {
  .filters {
    align-items: stretch;
  }

  .filters__field,
  .filters__search {
    flex: 1 1 100%;
  }
}
</style>
