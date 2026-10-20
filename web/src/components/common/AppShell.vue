<script setup lang="ts">
import { computed, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import type { RouteLocationRaw } from 'vue-router';
import { t } from '@/i18n/messages';
import type { MessageKey } from '@/i18n/messages';
import { useAuthStore } from '@/stores/auth';
import { useCustomerStore } from '@/stores/customers';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import { useOrganizationScope } from '@/composables/useOrganizationScope';
import { useTheme } from '@/composables/useTheme';
import type { ThemePreference } from '@/composables/useTheme';
import OrganizationSwitcher from './OrganizationSwitcher.vue';

/*
 * Direction C shell: a sticky 212px sidebar (brand, routed navigation, session footer)
 * next to a width-capped <main>. The two rules that are easy to break:
 *
 * 1. THE NAVIGATION IS DERIVED FROM THE ROUTER, not from a hand-written list of links.
 *    Every entry names a real route that exists in web/src/router/index.ts, and it is
 *    rendered only when the router still knows that name (`router.hasRoute`). A surface
 *    behind a build-time switch therefore disappears on its own if the route is
 *    conditionally registered. The four routes that are registered unconditionally but
 *    guarded by `meta.feature: 'releaseOperations'` (the guard answers NotFound) still
 *    need the explicit `operationsEnabled` check, because `hasRoute` is true for them;
 *    dropping that check would ship links that 404.
 *
 * 2. THE SHELL DOES NOT RENDER A PAGE TITLE. Pages own their heading through
 *    PageHeader.vue (or their own header); an earlier shell repeated the page title in
 *    the top bar, so every page showed it twice. Breadcrumbs belong to the page for the
 *    same reason: only the page knows what its identifiers mean.
 *
 * The stuck-lock route is behind the release-operations kill switch, so its entry must be
 * too (otherwise the link is visible and answers NotFound).
 */
const operationsEnabled = import.meta.env.VITE_ENABLE_RELEASE_OPERATIONS !== 'false';

const route = useRoute();
const auth = useAuthStore();
const customers = useCustomerStore();
const authorization = useEmergencyAuthorizationStore();
const router = useRouter();
const customerId = computed(() => (typeof route.params.customerId === 'string' ? route.params.customerId : ''));
const clusterRoutingEnabled = import.meta.env.VITE_FEATURE_CLUSTER_ROUTING !== 'false';

const { theme: themePreference, resolvedTheme, toggle: toggleTheme } = useTheme();

const THEME_LABEL_KEYS: Record<ThemePreference, MessageKey> = {
  system: 'shell.theme.system',
  light: 'shell.theme.light',
  dark: 'shell.theme.dark',
};
const currentThemeLabel = computed(() => t(THEME_LABEL_KEYS[themePreference.value]));
const themeToggleLabel = computed(() => t('shell.theme.toggle', { theme: currentThemeLabel.value }));

interface NavItem {
  /** Route name from the router's record list; also the availability probe. */
  routeName: string;
  label: string;
  to: RouteLocationRaw;
}

interface NavGroup {
  key: string;
  title: string;
  items: NavItem[];
}

/** A group before the unavailable entries are dropped (they are built as null). */
type DraftNavGroup = Omit<NavGroup, 'items'> & { items: Array<NavItem | null> };

/** One entry, or nothing when the feature switch or the capability says no. */
function navItem(routeName: string, label: string, to: RouteLocationRaw, enabled: boolean): NavItem | null {
  return enabled && router.hasRoute(routeName) ? { routeName, label, to } : null;
}

const navGroups = computed<NavGroup[]>(() => {
  const groups: DraftNavGroup[] = [
    {
      key: 'release',
      title: t('shell.nav.group.release'),
      items: [
        navItem('OperationCenter', t('operationCenter.title'), { name: 'OperationCenter' }, operationsEnabled),
        navItem('Definitions', '发布定义', { name: 'Definitions' }, operationsEnabled),
        navItem('Bundles', '发布 Bundle', { name: 'Bundles' }, operationsEnabled && auth.canReadBundles),
        navItem('ArtifactLifecycle', '制品生命周期', { name: 'ArtifactLifecycle' }, operationsEnabled && auth.canRunCleanup),
        navItem('StuckLocks', '紧急锁', { name: 'StuckLocks' }, operationsEnabled),
      ],
    },
    {
      key: 'assets',
      title: t('shell.nav.group.assets'),
      items: [
        navItem('CustomerList', t('nav.customers'), { name: 'CustomerList' }, true),
        navItem(
          'ClusterList',
          t('nav.clusters'),
          { name: 'ClusterList', params: { customerId: customerId.value } },
          clusterRoutingEnabled && customerId.value !== '',
        ),
        navItem('Audit', t('nav.audit'), { name: 'Audit' }, true),
      ],
    },
    {
      key: 'governance',
      title: t('shell.nav.group.governance'),
      items: [
        navItem('TrustPolicy', '信任根', { name: 'TrustPolicy' }, true),
        navItem('GovernanceBindings', '授权绑定', { name: 'GovernanceBindings' }, auth.canReadBindings),
        navItem('OrganizationMembers', '组织成员', { name: 'OrganizationMembers' }, true),
        navItem('LocalUsers', t('localUser.page.title'), { name: 'LocalUsers' }, auth.canManageLocalUsers),
        navItem('ChangePassword', '修改密码', { name: 'ChangePassword' }, true),
      ],
    },
  ];
  return groups
    .map((group) => ({ ...group, items: group.items.filter((entry): entry is NavItem => entry !== null) }))
    .filter((group) => group.items.length > 0);
});

// REQ-033 D-72: relink the customer selection, reload the bootstrap snapshot and
// drop the organization-domain caches whenever SwitchOrganization completes.
useOrganizationScope({
  resetCustomers: () => customers.reset(),
  reloadCustomers: () => customers.loadList(),
  resetAuthorization: () => authorization.reset(),
  reloadAuthorization: (organizationId, scopedCustomerId) => authorization.load(organizationId, scopedCustomerId),
  customerId: () => customerId.value,
});

async function handleLogout(): Promise<void> {
  await auth.logout();
  await router.replace({ name: 'Login' });
}

/*
 * The rail is a disclosure below 1024px. A drawer is the only shape that survives 375px:
 * the old horizontal bar kept every entry in the flow and the content got whatever was
 * left (163px at 375). The state is local because nothing else needs to read it, and it
 * closes on navigation so a tapped link cannot leave the panel covering the page it just
 * opened.
 */
const menuOpen = ref(false);

watch(
  () => route.fullPath,
  () => {
    menuOpen.value = false;
  },
);
</script>

<template>
  <div class="app-shell">
    <aside class="app-shell__aside" @keydown.escape="menuOpen = false">
      <RouterLink class="app-shell__brand" :to="{ name: 'Home' }">
        <span class="app-shell__mark" aria-hidden="true">
          <svg viewBox="0 0 24 24" width="13" height="13" focusable="false" aria-hidden="true">
            <path d="M5 19V5h9.2a4.6 4.6 0 0 1 0 9.2H9.6L16 19h-3.3l-4.9-4.8V19H5Z" fill="currentColor" />
          </svg>
        </span>
        <span class="app-shell__brand-name">Release Manager</span>
      </RouterLink>

      <!-- The drawer control. A native <button>, so Tab/Enter/Space/aria-expanded come
           free; `aria-controls` names the panel that is hidden while collapsed. It is
           display:none above 1024px, where the rail is always visible. -->
      <button
        class="app-shell__menu"
        type="button"
        :aria-expanded="menuOpen"
        aria-controls="app-shell-panel"
        @click="menuOpen = !menuOpen"
      >
        <span class="app-shell__menu-icon" aria-hidden="true">☰</span>
        <span>{{ t('shell.nav.menu') }}</span>
      </button>

      <div id="app-shell-panel" class="app-shell__panel" :class="{ 'app-shell__panel--open': menuOpen }">
        <nav class="app-shell__nav" :aria-label="t('shell.nav.label')">
          <div v-for="group in navGroups" :key="group.key" class="app-shell__group">
            <p class="app-shell__group-title">{{ group.title }}</p>
            <RouterLink v-for="link in group.items" :key="link.routeName" class="app-shell__link" :to="link.to">
              {{ link.label }}
            </RouterLink>
          </div>
        </nav>

        <div class="app-shell__footer">
          <OrganizationSwitcher />
          <div class="app-shell__identity">
            <strong>{{ auth.user?.username }}</strong>
            <span>{{ auth.activeOrganization?.name }}</span>
          </div>
          <button
            class="app-shell__control app-shell__theme"
            type="button"
            :aria-label="themeToggleLabel"
            :title="themeToggleLabel"
            @click="toggleTheme()"
          >
            <span class="app-shell__theme-icon" aria-hidden="true">{{ resolvedTheme === 'dark' ? '☀' : '☾' }}</span>
            <span>{{ currentThemeLabel }}</span>
          </button>
          <button class="app-shell__control app-shell__logout" type="button" @click="handleLogout">
            {{ t('shell.signOut') }}
          </button>
        </div>
      </div>
    </aside>

    <main class="app-shell__main">
      <slot />
    </main>
  </div>
</template>

<style scoped>
.app-shell {
  display: flex;
  align-items: stretch;
  min-height: 100vh;
  background: var(--bg);
  color: var(--tx);
}

.app-shell__aside {
  position: sticky;
  top: 0;
  display: flex;
  flex: none;
  flex-direction: column;
  gap: var(--space-5);
  width: 212px;
  height: 100vh;
  padding: 16px 12px;
  overflow-y: auto;
  border-right: 1px solid var(--bd);
  background: var(--bg);
}

.app-shell__brand {
  display: flex;
  gap: 8px;
  align-items: center;
  padding: 2px 8px;
  border-radius: var(--r);
  color: var(--tx);
  text-decoration: none;
}

.app-shell__brand:hover {
  background: var(--sf2);
}

.app-shell__mark {
  display: grid;
  flex: none;
  place-items: center;
  width: 22px;
  height: 22px;
  border-radius: 6px;
  background: var(--pr);
  color: var(--on);
}

.app-shell__brand-name {
  font-size: var(--font-size-md);
  font-weight: var(--font-weight-heading);
  letter-spacing: -0.01em;
}

.app-shell__nav {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 18px;
  min-height: 0;
}

.app-shell__group {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.app-shell__group-title {
  margin: 0 0 2px;
  padding: 0 8px;
  color: var(--mu);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-medium);
  letter-spacing: 0.04em;
}

.app-shell__link {
  padding: 6px 8px;
  border-radius: 6px;
  color: var(--mu);
  font-size: var(--font-size-md);
  text-decoration: none;
  white-space: nowrap;
}

.app-shell__link:hover {
  background: var(--sf2);
  color: var(--tx);
}

/* RouterLink sets this class itself; the sidebar marks the current surface instead of
   adding a second "active" state the caller would have to keep in sync. */
.app-shell__link.router-link-active {
  background: var(--sf2);
  color: var(--tx);
  font-weight: var(--font-weight-medium);
}

.app-shell__footer {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-top: auto;
  padding-top: 12px;
  border-top: 1px solid var(--bd);
}

.app-shell__identity {
  display: grid;
  padding: 0 8px;
  overflow-wrap: anywhere;
  font-size: var(--font-size-xs);
}

.app-shell__identity strong {
  color: var(--tx);
  font-weight: var(--font-weight-medium);
}

.app-shell__identity span {
  color: var(--mu);
}

.app-shell__control {
  display: flex;
  gap: 8px;
  align-items: center;
  padding: 6px 8px;
  border: 1px solid var(--bd);
  border-radius: var(--r);
  background: var(--sf);
  color: var(--tx);
  font-size: var(--font-size-xs);
  text-align: left;
  cursor: pointer;
}

.app-shell__control:hover {
  border-color: var(--mu);
}

.app-shell__theme-icon {
  flex: none;
  width: 1em;
  text-align: center;
}

.app-shell__logout {
  color: var(--mu);
}

.app-shell__logout:hover {
  border-color: var(--er);
  color: var(--er);
}

.app-shell__main {
  flex: 1;
  min-width: 0;
  /* Direction C settled on ONE content width (the old shell capped it at 90rem/1440px,
     which produced 120-character table rows). Pages narrower than this keep their own cap
     — ArtifactLifecycle 56rem, OperatorEnroll 54, Definitions 52, Bundles 50, LocalUsers
     48, StuckLocks 46, ChangePassword 32, Login 26, Init 28 — and the one page that was
     wider (ValuesEditorPage, 96rem) converges here. */
  max-width: 1180px;
  padding: 24px 32px 48px;
}

/* The drawer control. display:none above 1024px: the rail is always visible there, so a
   menu button would be a second way to do nothing. */
.app-shell__menu {
  display: none;
  flex: none;
  gap: 8px;
  align-items: center;
  margin-left: auto;
  padding: 6px 10px;
  border: 1px solid var(--bd);
  border-radius: var(--r);
  background: var(--sf);
  color: var(--tx);
  font-size: var(--font-size-sm);
  cursor: pointer;
}

.app-shell__menu[aria-expanded='true'] {
  background: var(--sf2);
}

.app-shell__menu-icon {
  font-size: var(--font-size-md);
  line-height: 1;
}

/* Nav + footer travel together so the drawer can hide and show both in one move, and so
   the desktop column still stretches (nav flex:1, footer margin-top:auto). */
.app-shell__panel {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: var(--space-5);
  min-height: 0;
}

/*
 * Collapse at 1024px, not at the previous 900px: between 900 and 1024 the fixed 212px
 * rail plus a wide table page still forced the DOCUMENT to scroll sideways, and below
 * that the old horizontal bar left the content 163px at 375. The drawer is the shape
 * that survives both: the bar is two items (brand + toggle), and the navigation only
 * occupies space while it is open.
 */
@media (max-width: 1024px) {
  .app-shell {
    flex-direction: column;
  }

  .app-shell__aside {
    position: sticky;
    top: 0;
    z-index: 10;
    flex-direction: row;
    flex-wrap: wrap;
    gap: 8px 12px;
    align-items: center;
    width: 100%;
    height: auto;
    padding: 8px 12px;
    /* The panel clips its own overflow; the bar must not grow a scrollbar of its own. */
    overflow: visible;
    border-right: 0;
    border-bottom: 1px solid var(--bd);
  }

  .app-shell__menu {
    display: inline-flex;
  }

  .app-shell__panel {
    display: none;
    flex: 1 1 100%;
    flex-basis: 100%;
    max-height: calc(100vh - 4.5rem);
    padding: 4px 0 8px;
    overflow-y: auto;
  }

  .app-shell__panel--open {
    display: flex;
  }

  .app-shell__main {
    padding: 16px 16px 32px;
  }
}

@media (max-width: 480px) {
  .app-shell__main {
    padding: 12px 12px 32px;
  }

  .app-shell__brand-name {
    font-size: var(--font-size-sm);
  }
}
</style>
