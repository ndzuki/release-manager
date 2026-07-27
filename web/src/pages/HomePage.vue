<script setup lang="ts">
import { shallowRef } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import { useAuthStore } from '@/stores/auth';

const auth = useAuthStore();
const showWelcome = shallowRef(true);
</script>

<template>
  <section class="home-page">
    <header>
      <p class="home-page__eyebrow">Active organization</p>
      <h1>{{ auth.activeOrganization?.name ?? 'Release Manager' }}</h1>
      <p>Signed in as <strong>{{ auth.user?.username }}</strong>.</p>
    </header>

    <EmptyState
      v-if="showWelcome"
      title="No release activity yet"
      message="Release activity for this organization will appear here."
      action-label="Dismiss"
      @action="showWelcome = false"
    />
  </section>
</template>

<style scoped>
.home-page {
  display: grid;
  gap: 1.5rem;
}

.home-page h1,
.home-page p {
  margin: 0;
}

.home-page__eyebrow {
  color: var(--color-muted, #64748b);
  font-size: 0.75rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}
</style>
