<script setup lang="ts">
import { storeToRefs } from 'pinia';
import { RouterView, useRouter } from 'vue-router';
import { useAuthStore } from '@/stores/auth';
import { useSessionExpiry } from '@/composables/useSessionExpiry';
import AppShell from '@/components/common/AppShell.vue';
import LoadingState from '@/components/common/LoadingState.vue';

const auth = useAuthStore();
const router = useRouter();
const { expiresAt } = storeToRefs(auth);

useSessionExpiry({
  expiresAt,
  onExpired: () => {
    auth.clearSession('expired');
    void router.replace({ name: 'Login', query: { reason: 'expired' } });
  },
});
</script>

<template>
  <LoadingState v-if="auth.status === 'idle' || auth.status === 'initializing'" message="Restoring session…" />
  <AppShell v-else-if="auth.isAuthenticated">
    <RouterView />
  </AppShell>
  <RouterView v-else />
</template>
