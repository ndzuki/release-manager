<script setup lang="ts">
import { computed } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import { t } from '@/i18n/messages';
import { useAuthStore } from '@/stores/auth';
import { useCustomerStore } from '@/stores/customers';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import { useOrganizationScope } from '@/composables/useOrganizationScope';
import OrganizationSwitcher from './OrganizationSwitcher.vue';

// The stuck-lock route is behind the release-operations kill switch, so its entry
// must be too (otherwise the link is visible and answers NotFound).
const operationsEnabled = import.meta.env.VITE_ENABLE_RELEASE_OPERATIONS !== 'false';

const route = useRoute();
const auth = useAuthStore();
const customers = useCustomerStore();
const authorization = useEmergencyAuthorizationStore();
const router = useRouter();
const customerId = computed(() => typeof route.params.customerId === 'string' ? route.params.customerId : '');
const clusterRoutingEnabled = import.meta.env.VITE_FEATURE_CLUSTER_ROUTING !== 'false';

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
</script>

<template>
  <div class="app-shell">
    <header class="app-shell__header">
      <RouterLink class="app-shell__brand" :to="{ name: 'Home' }">Release Manager</RouterLink>
      <nav class="app-shell__nav" :aria-label="t('shell.nav.label')">
        <RouterLink :to="{ name: 'CustomerList' }">{{ t('nav.customers') }}</RouterLink>
        <RouterLink v-if="clusterRoutingEnabled && customerId" :to="{ name: 'ClusterList', params: { customerId } }">{{ t('nav.clusters') }}</RouterLink>
        <!-- Audit needs no capability gate: the read policy is modePrincipalScope
             (any authenticated principal, scoped to their own organization) and
             the default role matrix grants `audit read` to all four roles — see
             internal/auth/procedure_policy.go and internal/auth/casbin.go. The
             remaining 403/503 comes from release-auth's own Authorize call
             (fail-closed), which the server owns. The plan's N3 assumed a 403
             here; that observation came from the bearer-only audit interceptor
             that TASK-174 fixed. -->
        <RouterLink :to="{ name: 'Audit' }">{{ t('nav.audit') }}</RouterLink>
      </nav>
      <div class="app-shell__session">
        <OrganizationSwitcher />
        <div class="app-shell__identity">
          <strong>{{ auth.user?.username }}</strong>
          <span>{{ auth.activeOrganization?.name }}</span>
        </div>
        <RouterLink v-if="operationsEnabled && auth.canRunCleanup" class="app-shell__password" :to="{ name: 'ArtifactLifecycle' }">制品生命周期</RouterLink>
        <RouterLink v-if="operationsEnabled && auth.canReadBundles" class="app-shell__password" :to="{ name: 'Bundles' }">发布 Bundle</RouterLink>
        <RouterLink v-if="operationsEnabled" class="app-shell__password" :to="{ name: 'OperationCenter' }">{{ t('operationCenter.title') }}</RouterLink>
        <RouterLink v-if="operationsEnabled" class="app-shell__password" :to="{ name: 'Definitions' }">发布定义</RouterLink>
        <RouterLink v-if="operationsEnabled" class="app-shell__password" :to="{ name: 'StuckLocks' }">紧急锁</RouterLink>
        <RouterLink class="app-shell__password" :to="{ name: 'TrustPolicy' }">信任根</RouterLink>
        <RouterLink v-if="auth.canReadBindings" class="app-shell__password" :to="{ name: 'GovernanceBindings' }">授权绑定</RouterLink>
        <RouterLink class="app-shell__password" :to="{ name: 'OrganizationMembers' }">组织成员</RouterLink>
        <RouterLink
          v-if="auth.canManageLocalUsers"
          class="app-shell__password"
          :to="{ name: 'LocalUsers' }"
        >{{ t('localUser.page.title') }}</RouterLink>
        <RouterLink class="app-shell__password" :to="{ name: 'ChangePassword' }">修改密码</RouterLink>
        <button class="app-shell__logout" type="button" @click="handleLogout">{{ t('shell.signOut') }}</button>
      </div>
    </header>
    <main class="app-shell__main">
      <slot />
    </main>
  </div>
</template>

<style scoped>
.app-shell {
  display: flex;
  flex-direction: column;
  min-height: 100vh;
  background: var(--color-bg);
}

.app-shell__header {
  display: grid;
  grid-template-columns: auto 1fr auto;
  align-items: center;
  gap: 2rem;
  min-height: 4.5rem;
  padding: 0.75rem 1.5rem;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-surface);
}

.app-shell__brand {
  color: var(--color-text);
  font-size: var(--font-size-lg);
  font-weight: 700;
  text-decoration: none;
}

.app-shell__nav {
  display: flex;
  gap: 1rem;
}

.app-shell__nav a {
  color: var(--color-text-secondary);
  text-decoration: none;
}

.app-shell__session {
  display: flex;
  align-items: center;
  gap: 1rem;
}

.app-shell__identity {
  display: grid;
  min-width: 8rem;
  font-size: var(--font-size-sm);
}

.app-shell__identity span {
  color: var(--color-muted);
}

.app-shell__logout,
.app-shell__password {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  color: var(--color-text);
  font-size: var(--font-size-sm);
  text-decoration: none;
  cursor: pointer;
}

.app-shell__main {
  flex: 1;
  width: min(100%, 90rem);
  margin: 0 auto;
  padding: 2rem 1.5rem;
}

@media (max-width: 56rem) {
  .app-shell__header {
    grid-template-columns: 1fr;
    gap: 0.75rem;
  }

  .app-shell__session {
    flex-wrap: wrap;
  }
}
</style>
