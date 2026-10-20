<script setup lang="ts">
import { useRouter } from 'vue-router';
import { useAuthStore } from '@/stores/auth';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import { t } from '@/i18n/messages';

const auth = useAuthStore();
const router = useRouter();
const message = auth.consumeForbiddenMessage() ?? undefined;
</script>

<template>
  <!-- This page renders inside AppShell, so its root is not <main>; the wrapper only gives
       the state the same 32rem centred column NotFoundPage uses. `heading-level="h1"`
       makes the state's own title the page heading instead of adding a second one
       (TASK-394). -->
  <div class="forbidden-page">
    <ForbiddenState
      heading-level="h1"
      :message="message"
      :action-label="t('action.returnHome')"
      @action="router.push({ name: 'Home' })"
    />
  </div>
</template>

<style scoped>
.forbidden-page {
  /* Shared error-page shell with NotFoundPage. */
  max-width: 32rem;
  margin: 48px auto;
}
</style>
