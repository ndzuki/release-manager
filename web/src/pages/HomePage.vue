<script setup lang="ts">
import { computed, onMounted, ref } from 'vue';
import { useRouter } from 'vue-router';
import { t } from '@/i18n/messages';
import { useAuthStore } from '@/stores/auth';
import PageHeader from '@/components/common/PageHeader.vue';
import StatCard from '@/components/common/StatCard.vue';
import StatusTag from '@/components/common/StatusTag.vue';
import AppDialog from '@/components/common/AppDialog.vue';
import HomeOperationsPanel from '@/components/home/HomeOperationsPanel.vue';
import HomeClusterPanel from '@/components/home/HomeClusterPanel.vue';
import { useHomeOverview } from '@/components/home/useHomeOverview';
import type { InventoryRelease } from '@/components/home/homeOverview';

/*
 * Home overview (TASK-394, direction C).
 *
 * The page answers four questions and then gets out of the way: how many releases
 * still need convergence, how much work is in flight, how the last week went, and
 * whether any emergency change is waiting for an explicit decision. Below them sit
 * the in-progress queue and the per-cluster health the operation centre and the
 * release inventory each know how to read.
 *
 * Two rules the layout enforces, both of them honesty rules rather than styling:
 *
 * 1. A number is rendered only when the read that produces it succeeded. Every
 *    metric card falls back to "—" with the reason in its hint (接口未提供 /
 *    加载失败 / 功能未启用) instead of a 0 that reads like a healthy state. The
 *    7-day success rate is permanently in that bucket: no cross-release terminal
 *    operation query exists, and the audit list is cursor-paginated with no total,
 *    so computing a rate would mean inventing a sample size.
 * 2. The navigation tiles stay. They were the only way into most surfaces from
 *    the console's front door, so the redesign tucks them into a "quick links"
 *    card instead of replacing them with the overview.
 */
const auth = useAuthStore();
const router = useRouter();

const operationsEnabled = import.meta.env.VITE_ENABLE_RELEASE_OPERATIONS !== 'false';
const operatorManagementEnabled = import.meta.env.VITE_FEATURE_OPERATOR_MANAGEMENT !== 'false';

const { summary, inventory, inventoryState, stuckLocksState, operationsState, feed, reload } =
  useHomeOverview();

onMounted(() => {
  void reload();
});

/** Falls back to a product name rather than an empty heading when the session has no org yet. */
const organizationName = computed(() => auth.activeOrganization?.name ?? 'Release Manager');

const inventoryReady = computed(() => inventoryState.value === 'ok');
const operationsReady = computed(() => operationsState.value === 'ok');

function sourceHint(state: 'loading' | 'ok' | 'error' | 'off'): string {
  switch (state) {
    case 'loading':
      return t('home.overview.loading');
    case 'error':
      return t('home.overview.loadFailed');
    case 'off':
      return t('home.overview.featureOff');
    default:
      return '';
  }
}

const pendingValue = computed(() => (inventoryReady.value ? summary.value.pendingConvergence : '—'));
const pendingTone = computed(() => (inventoryReady.value && summary.value.pendingConvergence > 0 ? 'wn' : 'neutral'));
const pendingHint = computed(() =>
  inventoryReady.value
    ? t('home.stat.pendingConvergenceHint', { count: summary.value.releaseTotal })
    : sourceHint(inventoryState.value),
);

const operationsValue = computed(() => {
  if (!operationsReady.value) return '—';
  const count = summary.value.operations.length;
  return summary.value.operationsTruncated ? `${count}+` : String(count);
});
const operationsHint = computed(() => {
  if (operationsReady.value) return t('home.stat.activeOperationsHint', { count: summary.value.dispatching });
  switch (operationsState.value) {
    case 'maintenance':
      return t('home.todo.maintenance');
    case 'forbidden':
      return t('home.todo.forbidden');
    case 'error':
      return t('home.overview.loadFailed');
    case 'off':
      return t('home.overview.featureOff');
    default:
      return t('home.overview.loading');
  }
});

const emergencyValue = computed(() => (stuckLocksState.value === 'ok' ? summary.value.stuckLocks : '—'));
const emergencyTone = computed(() => (stuckLocksState.value === 'ok' && summary.value.stuckLocks > 0 ? 'er' : 'neutral'));
const emergencyHint = computed(() =>
  stuckLocksState.value === 'ok' ? t('home.stat.emergencyHint') : sourceHint(stuckLocksState.value),
);

/*
 * The live indicator states its OWN coverage. Only the clusters the health panel
 * renders are probed (ListOperators is cluster-scoped), so a partial probe says
 * "已探测 K/M 个集群" instead of presenting a subset as the organization total.
 */
const liveVisible = computed(() => operatorManagementEnabled && inventoryState.value !== 'loading');
const liveLabel = computed(() => {
  switch (summary.value.operatorCoverage) {
    case 'complete':
      return t('home.live.online', { count: summary.value.onlineOperators });
    case 'partial':
      return t('home.live.partial', {
        count: summary.value.onlineOperators,
        probed: summary.value.probedClusters,
        total: summary.value.clustersTotal,
      });
    default:
      return t('home.live.unavailable');
  }
});
const liveTone = computed(() =>
  summary.value.operatorCoverage !== 'unavailable' && summary.value.onlineOperators > 0 ? 'ok' : 'neutral',
);

const clusterNames = computed<Record<string, string>>(() =>
  Object.fromEntries(summary.value.clusters.map((cluster) => [cluster.clusterId, cluster.name])),
);

/*
 * "Create operation" needs a release as context and the create route only exists
 * when the release inventory + values revision switches are on. The dialog picks
 * from the inventory this page already read, so it can never send the form to a
 * release that does not exist.
 */
const createRouteAvailable = computed(() => router.hasRoute('OperationCreate'));
const creatableReleases = computed<InventoryRelease[]>(() =>
  [...inventory.value]
    .filter((release) => release.releaseDefinitionId !== '')
    .sort(
      (left, right) =>
        left.releaseName.localeCompare(right.releaseName, 'zh-Hans-CN') ||
        left.clusterName.localeCompare(right.clusterName, 'zh-Hans-CN'),
    ),
);
const canCreate = computed(
  () => auth.canCreateReleaseOperation && createRouteAvailable.value && creatableReleases.value.length > 0,
);
const createOpen = ref(false);
const selectedIndex = ref(0);

function openCreate(): void {
  selectedIndex.value = 0;
  createOpen.value = true;
}

function confirmCreate(): void {
  const release = creatableReleases.value[selectedIndex.value];
  if (!release) return;
  createOpen.value = false;
  void router.push({
    name: 'OperationCreate',
    params: {
      customerId: release.customerId,
      clusterId: release.clusterId,
      releaseId: release.releaseDefinitionId,
    },
    query: {
      customerName: release.customerName,
      clusterName: release.clusterName,
      releaseName: release.releaseDefinitionName || release.releaseName,
      currentRevision: release.revision,
    },
  });
}

interface Tile {
  name: string;
  label: string;
  hint: string;
}

/*
 * The entry grid is the page's navigation fallback: every tile names a real route,
 * and the ones that need a capability are hidden for roles that do not hold it,
 * mirroring the shell. It is kept inside a card rather than as the whole page.
 */
const tiles = computed<Tile[]>(() => {
  const list: Tile[] = [
    { name: 'CustomerList', label: '客户', hint: '进入客户 → 集群 → 发布的层级' },
    { name: 'Audit', label: '审计', hint: '按组织检索已脱敏的审计事件' },
  ];
  if (operationsEnabled) {
    list.push({ name: 'OperationCenter', label: t('operationCenter.title'), hint: t('operationCenter.hint') });
    list.push({ name: 'Definitions', label: '发布定义', hint: 'Promotion Mapping 与定义设置' });
    if (auth.canReadBundles) {
      list.push({ name: 'Bundles', label: '发布 Bundle', hint: '内容摘要、镜像绑定与证据引用' });
    }
    list.push({ name: 'StuckLocks', label: '紧急锁', hint: '效果未知且已过观察窗口的紧急操作' });
  }
  if (auth.canRunCleanup) {
    list.push({ name: 'ArtifactLifecycle', label: '制品生命周期', hint: '保留期 GC 与归档恢复' });
  }
  list.push({ name: 'TrustPolicy', label: '信任根', hint: '签名信任策略与轮换' });
  if (auth.canReadBindings) {
    list.push({ name: 'GovernanceBindings', label: '授权绑定', hint: '组织与客户的绑定及能力授予' });
  }
  if (auth.canManageLocalUsers) {
    list.push({ name: 'LocalUsers', label: t('localUser.page.title'), hint: t('localUser.page.eyebrow') });
  }
  list.push({ name: 'OrganizationMembers', label: '组织成员', hint: '成员与角色' });
  list.push({ name: 'ChangePassword', label: '修改密码', hint: '修改后会吊销全部会话' });
  return list;
});
</script>

<template>
  <section class="home-page">
    <PageHeader :title="organizationName" :subtitle="t('home.overview.subtitle')">
      <template #actions>
        <button v-if="canCreate" type="button" class="home-page__primary" data-testid="home-create" @click="openCreate">
          {{ t('home.create.open') }}
        </button>
        <StatusTag
          v-if="liveVisible"
          :tone="liveTone"
          :pulse="liveTone === 'ok'"
          :label="liveLabel"
          data-testid="home-live"
        />
      </template>
    </PageHeader>

    <div class="home-page__stats">
      <StatCard
        :label="t('home.stat.pendingConvergence')"
        :value="pendingValue"
        :hint="pendingHint"
        :tone="pendingTone"
        data-testid="home-stat-pending"
      />
      <StatCard
        :label="t('home.stat.activeOperations')"
        :value="operationsValue"
        :hint="operationsHint"
        data-testid="home-stat-operations"
      />
      <StatCard
        :label="t('home.stat.successRate')"
        value="—"
        :hint="t('home.overview.unavailable')"
        data-testid="home-stat-success"
      />
      <StatCard
        :label="t('home.stat.emergency')"
        :value="emergencyValue"
        :hint="emergencyHint"
        :tone="emergencyTone"
        data-testid="home-stat-emergency"
      />
    </div>

    <div class="home-page__columns">
      <HomeOperationsPanel
        v-if="operationsEnabled"
        :items="feed.items.value"
        :truncated="summary.operationsTruncated"
        :state="operationsState"
        :cluster-names="clusterNames"
        @retry="reload()"
      />
      <HomeClusterPanel
        :clusters="summary.clusters"
        :clusters-total="summary.clustersTotal"
        :state="inventoryState"
        @retry="reload()"
      />
    </div>

    <section class="home-page__quicklinks" aria-labelledby="home-quicklinks-title">
      <h2 id="home-quicklinks-title" class="home-page__quicklinks-title">{{ t('home.quickLinks.title') }}</h2>
      <nav class="home-page__tiles" :aria-label="t('home.workbench')">
        <RouterLink
          v-for="tile in tiles"
          :key="tile.name"
          class="home-page__tile"
          :to="{ name: tile.name }"
          :data-testid="`home-tile-${tile.name}`"
        >
          <strong>{{ tile.label }}</strong>
          <span>{{ tile.hint }}</span>
        </RouterLink>
      </nav>
    </section>

    <AppDialog v-if="canCreate" :open="createOpen" :title="t('home.create.title')" @close="createOpen = false">
      <div class="home-page__dialog">
        <p class="home-page__dialog-hint">{{ t('home.create.hint') }}</p>
        <label class="home-page__dialog-label" for="home-create-release">{{ t('home.create.selectLabel') }}</label>
        <select id="home-create-release" v-model.number="selectedIndex" class="home-page__dialog-select">
          <option v-for="(release, index) in creatableReleases" :key="`${release.clusterId}-${release.releaseDefinitionId}`" :value="index">
            {{ release.releaseDefinitionName || release.releaseName }} · {{ release.clusterName }}
          </option>
        </select>
      </div>
      <template #footer>
        <button type="button" class="home-page__dialog-cancel" @click="createOpen = false">{{ t('action.cancel') }}</button>
        <button
          type="button"
          class="home-page__primary"
          data-testid="home-create-confirm"
          :disabled="creatableReleases.length === 0"
          @click="confirmCreate"
        >
          {{ t('home.create.confirm') }}
        </button>
      </template>
    </AppDialog>
  </section>
</template>

<style scoped>
.home-page {
  display: grid;
  gap: var(--space-5);
}

.home-page__primary {
  padding: 6px 12px;
  border: 1px solid var(--pr);
  border-radius: var(--r);
  background: var(--pr);
  color: var(--on);
  font-size: var(--font-size-md);
  font-weight: var(--font-weight-medium);
  cursor: pointer;
}

.home-page__primary:hover {
  background: var(--color-primary-hover);
}

.home-page__stats {
  display: grid;
  gap: 12px;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
}

.home-page__columns {
  display: grid;
  gap: 16px;
  grid-template-columns: 1.5fr 1fr;
}

.home-page__quicklinks {
  display: grid;
  gap: 12px;
  padding: 16px;
  border: 1px solid var(--bd);
  border-radius: var(--r);
  background: var(--sf);
}

.home-page__quicklinks-title {
  margin: 0;
  color: var(--tx);
  font-size: var(--font-size-md);
  font-weight: var(--font-weight-heading);
}

.home-page__tiles {
  display: grid;
  gap: var(--space-3);
  grid-template-columns: repeat(auto-fill, minmax(14rem, 1fr));
}

.home-page__tile {
  display: grid;
  gap: var(--space-1);
  padding: 10px 12px;
  border: 1px solid var(--bd);
  border-radius: var(--r);
  background: var(--bg);
  color: var(--color-text);
  text-decoration: none;
}

.home-page__tile:hover {
  border-color: var(--pr);
}

.home-page__tile span {
  color: var(--mu);
  font-size: var(--font-size-sm);
}

.home-page__dialog {
  display: grid;
  gap: 10px;
}

.home-page__dialog-hint,
.home-page__dialog-label {
  margin: 0;
  color: var(--mu);
  font-size: var(--font-size-sm);
}

.home-page__dialog-select {
  max-width: 100%;
  padding: 6px 8px;
  border: 1px solid var(--color-border-strong);
  border-radius: 6px;
  background: var(--sf2);
  color: var(--tx);
  font-size: var(--font-size-md);
}

.home-page__dialog-cancel {
  padding: 6px 12px;
  border: 1px solid var(--bd);
  border-radius: var(--r);
  background: var(--sf2);
  color: var(--tx);
  font-size: var(--font-size-md);
  cursor: pointer;
}

@media (max-width: 900px) {
  .home-page__columns {
    grid-template-columns: 1fr;
  }
}
</style>
