<script setup lang="ts">
import { t } from '@/i18n/messages';

/*
 * Loading placeholder for the release inventory (TASK-394). It mirrors the direction-C
 * table it replaces — a framed header strip plus rows on the same 6-column rhythm — so
 * the page does not jump when the rows arrive. The accessible name comes from the
 * catalog; the whole shape is `aria-hidden` decoration inside one polite `role="status"`.
 */
</script>

<template>
  <div class="skeleton-table" :aria-label="t('state.loading.releases')" role="status">
    <div class="skeleton-table__head" aria-hidden="true">
      <span v-for="column in 6" :key="column" />
    </div>
    <div v-for="row in 5" :key="row" class="skeleton-table__row" aria-hidden="true">
      <span v-for="column in 6" :key="column" />
    </div>
  </div>
</template>

<style scoped>
.skeleton-table {
  overflow: hidden;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

.skeleton-table__head,
.skeleton-table__row {
  display: grid;
  grid-template-columns: 2.2fr 1fr 0.8fr 1fr 1.4fr 1fr;
  gap: var(--space-4);
  align-items: center;
  padding: var(--space-3) var(--space-5);
}

.skeleton-table__head {
  background: var(--color-surface-muted);
}

.skeleton-table__head span {
  height: 8px;
  border-radius: 4px;
  background: var(--color-border-strong);
}

.skeleton-table__row {
  border-top: 1px solid var(--color-border-strong);
}

.skeleton-table__row span {
  height: 12px;
  border-radius: 4px;
  background: linear-gradient(90deg, var(--color-border-strong), var(--color-surface-muted), var(--color-border-strong));
  background-size: 200% 100%;
  animation: shimmer 1.2s infinite;
}

@keyframes shimmer {
  to {
    background-position: -200% 0;
  }
}

/* Below the table's card breakpoint the skeleton becomes cards too, so the layouts match. */
@media (max-width: 40rem) {
  .skeleton-table {
    display: grid;
    gap: var(--space-3);
    border: 0;
    background: transparent;
  }

  .skeleton-table__head {
    display: none;
  }

  .skeleton-table__row {
    grid-template-columns: 1fr 1fr;
    padding: var(--space-4);
    border: 1px solid var(--color-border-strong);
    border-radius: var(--radius-lg);
    background: var(--color-surface);
  }
}
</style>
