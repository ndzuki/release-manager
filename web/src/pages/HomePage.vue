<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed } from 'vue';
import { useAuthStore } from '@/stores/auth';

/*
 * Home workbench (plan N1).
 *
 * The page used to be a dead end: one welcome empty state whose only action was
 * "Dismiss" and not a single link, so the console's entry point led nowhere. This is
 * a plain entry grid instead — every tile is a real destination, and the ones that
 * need a capability are hidden for roles that do not hold it, mirroring the shell.
 *
 * Deliberately no counters: a cross-cluster "todo" number has no data source today,
 * and an invented number would be worse than none.
 */
const auth = useAuthStore();

const operationsEnabled = import.meta.env.VITE_ENABLE_RELEASE_OPERATIONS !== 'false';

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
