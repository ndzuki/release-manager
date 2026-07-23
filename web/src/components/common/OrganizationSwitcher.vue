<script setup lang="ts">
import { computed, shallowRef } from 'vue';
import { useRouter } from 'vue-router';
import { useAuthStore } from '@/stores/auth';

const auth = useAuthStore();
const router = useRouter();
const switching = shallowRef(false);
const errorMessage = shallowRef('');

const canSwitchOrganizations = computed(
  () => (auth.user?.roles.includes('platform_admin') ?? false) && auth.organizations.length > 1,
);
const selectedOrganizationId = computed({
  get: () => auth.user?.activeOrgId ?? '',
  set: (organizationId: string) => {
    void selectOrganization(organizationId);
  },
});

async function selectOrganization(organizationId: string): Promise<void> {
  if (!organizationId || organizationId === auth.user?.activeOrgId || switching.value) return;
  switching.value = true;
  errorMessage.value = '';
  try {
    await auth.switchOrganization(organizationId);
    await router.replace({ path: router.currentRoute.value.fullPath, query: { ...router.currentRoute.value.query } });
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : 'Unable to switch organization.';
  } finally {
    switching.value = false;
  }
}
</script>

<template>
  <div v-if="canSwitchOrganizations" class="organization-switcher">
    <label class="organization-switcher__label" for="active-organization">Organization</label>
    <select
      id="active-organization"
      v-model="selectedOrganizationId"
      class="organization-switcher__select"
      :disabled="switching || auth.organizations.length < 2"
      aria-describedby="organization-switch-error"
    >
      <option v-for="organization in auth.organizations" :key="organization.id" :value="organization.id">
        {{ organization.name }}
      </option>
    </select>
    <p v-if="errorMessage" id="organization-switch-error" class="organization-switcher__error" role="alert">
      {{ errorMessage }}
    </p>
  </div>
</template>

<style scoped>
.organization-switcher {
  display: grid;
  gap: 0.25rem;
}

.organization-switcher__label {
  font-size: 0.75rem;
  color: var(--color-muted, #64748b);
}

.organization-switcher__select {
  min-width: 12rem;
  padding: 0.4rem 0.6rem;
  border: 1px solid var(--color-border, #cbd5e1);
  border-radius: 0.375rem;
  background: var(--color-surface, #fff);
}

.organization-switcher__error {
  margin: 0;
  max-width: 18rem;
  color: var(--color-error, #b91c1c);
  font-size: 0.75rem;
}
</style>
