<script setup lang="ts">
import { t } from '@/i18n/messages';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import type { CustomerEvent } from '@/types/customer';

defineProps<{
  events: CustomerEvent[];
  loading?: boolean;
  error?: string | null;
}>();

const emit = defineEmits<{ retry: [] }>();

function eventLabel(eventType: string): string {
  return eventType.replace(/^customer_/, '').replace(/_/g, ' ');
}
</script>

<template>
  <section class="customer-history" aria-labelledby="customer-history-title">
    <header class="customer-history__header">
      <h2 id="customer-history-title">{{ t('customer.history.title') }}</h2>
    </header>
    <LoadingState v-if="loading" :message="t('customer.history.loading')" />
    <ErrorState v-else-if="error" :message="error">
      <button type="button" @click="emit('retry')">{{ t('action.retry') }}</button>
    </ErrorState>
    <EmptyState v-else-if="events.length === 0" :title="t('customer.history.empty')" :message="t('customer.history.noEvents')" />
    <ol v-else class="customer-history__list">
      <li v-for="event in events" :key="event.id" class="customer-history__item">
        <strong>{{ eventLabel(event.eventType) }}</strong>
        <time v-if="event.createdAt" :datetime="event.createdAt">{{ new Date(event.createdAt).toLocaleString() }}</time>
      </li>
    </ol>
  </section>
</template>

<style scoped>
.customer-history { display: grid; gap: 0.75rem; }
.customer-history__header h2 { margin: 0; font-size: var(--font-size-lg); }
.customer-history__list { display: grid; gap: 0.5rem; margin: 0; padding: 0; list-style: none; }
.customer-history__item { display: flex; justify-content: space-between; gap: 1rem; padding: 0.75rem; border: 1px solid var(--color-border); border-radius: 0.375rem; }
.customer-history__item time { color: var(--color-muted); font-size: var(--font-size-md); }
</style>
