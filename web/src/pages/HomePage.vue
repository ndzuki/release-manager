<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
import { computed, onMounted } from 'vue';
import { useAuthStore } from '@/stores/auth';
import { useOperationFeed } from '@/composables/useOperationFeed';

/*
 * Home workbench (plan N1).
 *
 * The page used to be a dead end: one welcome empty state whose only action was
 * "Dismiss" and not a single link, so the console's entry point led nowhere. This is
 * a plain entry grid instead — every tile is a real destination, and the ones that
 * need a capability are hidden for roles that do not hold it, mirroring the shell.
 *
 * The "waiting on me" panel is fed by the cross-release aggregate read (TASK-276), which
 * is the data source this page previously said did not exist.
 */
const auth = useAuthStore();

const operationsEnabled = import.meta.env.VITE_ENABLE_RELEASE_OPERATIONS !== 'false';

/*
 * One short page plus a one-row probe: the aggregate has no total, so "5+" is the only
 * honest count — asking for LIMIT+1 rows reveals "there is more" without a second call.
 */
const TODO_LIMIT = 5;

const {
  items: todoItems,
  nextPageToken: todoNextPageToken,
  loading: todoLoading,
  error: todoError,
  maintenance: todoMaintenance,
  forbidden: todoForbidden,
  empty: todoEmpty,
  load: loadTodo,
} = useOperationFeed({ pageSize: TODO_LIMIT + 1 });

const todoVisible = computed(() => todoItems.value.slice(0, TODO_LIMIT));
const todoHasMore = computed(
  () => todoItems.value.length > TODO_LIMIT || todoNextPageToken.value !== '',
);
const todoCount = computed(() =>
  todoHasMore.value
    ? t('home.todo.countMore', { count: todoVisible.value.length })
    : t('home.todo.count', { count: todoVisible.value.length }),
);

onMounted(() => {
  if (operationsEnabled) void loadTodo();
});

interface Tile {
  name: string;
  label: string;
  hint: string;
}

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
    <header class="home-page__header">
      <p class="home-page__eyebrow">{{ t('home.eyebrow') }}</p>
      <h1>{{ auth.activeOrganization?.name ?? 'Release Manager' }}</h1>
      <p>{{ t('home.signedInAs') }} <strong>{{ auth.user?.username }}</strong>.</p>
    </header>

    <!-- Cross-release pending work (TASK-277): a count plus the first few rows, read
         from the same aggregate the Operation centre uses. Never an invented number —
         the aggregate has no total, so the count becomes "N+" while a next page exists. -->
    <section v-if="operationsEnabled" class="home-page__todo" aria-labelledby="home-todo-title">
      <div class="home-page__todo-header">
        <h2 id="home-todo-title" class="home-page__todo-title">{{ t('home.todo.title') }}</h2>
        <RouterLink :to="{ name: 'OperationCenter' }">{{ t('home.todo.viewAll') }}</RouterLink>
      </div>

      <p v-if="todoMaintenance" class="home-page__todo-note" role="status">
        {{ t('home.todo.maintenance') }}
      </p>
      <p v-else-if="todoForbidden" class="home-page__todo-note" role="alert">
        {{ t('home.todo.forbidden') }}
      </p>
      <p v-else-if="todoLoading" class="home-page__todo-note" role="status">
        {{ t('home.todo.loading') }}
      </p>
      <div v-else-if="todoError" class="home-page__todo-note" role="alert">
        {{ t('home.todo.error') }}
        <button type="button" @click="loadTodo()">{{ t('action.retry') }}</button>
      </div>
      <p v-else-if="todoEmpty" class="home-page__todo-note">{{ t('home.todo.empty') }}</p>
      <template v-else>
        <p class="home-page__todo-count" role="status">{{ todoCount }}</p>
        <ul class="home-page__todo-list">
          <li v-for="item in todoVisible" :key="item.operationId">
            <RouterLink
              :to="{
                name: 'OperationCenterDetail',
                params: { operationId: item.operationId },
                query: { releaseName: item.releaseDefinitionName },
              }"
              :data-testid="`home-todo-${item.operationId}`"
            >
              <span class="home-page__todo-state">{{ statusLabel('operation', item.state) }}</span>
              <span>{{ statusLabel('operationType', item.operationType) }}</span>
              <span>{{ item.releaseDefinitionName || item.releaseDefinitionId }}</span>
              <span>{{ item.customerName || item.customerId }}</span>
            </RouterLink>
          </li>
        </ul>
      </template>
    </section>

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
</template>

<style scoped>
.home-page {
  display: grid;
  gap: var(--space-5);
}

.home-page__header {
  display: grid;
  gap: var(--space-1);
}

.home-page__todo {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

.home-page__todo-header {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  align-items: baseline;
  justify-content: space-between;
}

.home-page__todo-title,
.home-page__todo-count,
.home-page__todo-note {
  margin: 0;
}

.home-page__todo-title {
  font-size: var(--font-size-lg);
}

.home-page__todo-count {
  color: var(--color-muted-strong);
  font-weight: var(--font-weight-bold);
}

.home-page__todo-note {
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}

.home-page__todo-list {
  display: grid;
  gap: var(--space-2);
  margin: 0;
  padding: 0;
  list-style: none;
}

.home-page__todo-list a {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  align-items: center;
  color: var(--color-text);
  font-size: var(--font-size-sm);
  text-decoration: none;
}

.home-page__todo-list a:hover {
  color: var(--color-primary);
}

.home-page__todo-state {
  padding: 0.1rem var(--space-2);
  border-radius: var(--radius-xl);
  background: var(--color-surface-muted);
  font-weight: var(--font-weight-bold);
}

.home-page__eyebrow {
  color: var(--color-muted);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.home-page h1,
.home-page p {
  margin: 0;
}

.home-page__tiles {
  display: grid;
  gap: var(--space-3);
  grid-template-columns: repeat(auto-fill, minmax(16rem, 1fr));
}

.home-page__tile {
  display: grid;
  gap: var(--space-1);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
  color: var(--color-text);
  text-decoration: none;
}

.home-page__tile:hover {
  border-color: var(--color-primary);
}

.home-page__tile span {
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}
</style>
