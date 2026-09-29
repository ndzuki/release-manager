<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, shallowRef, watch } from 'vue';
import { storeToRefs } from 'pinia';
import { useRoute, useRouter } from 'vue-router';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import OperatorFilters from '@/components/operators/OperatorFilters.vue';
import OperatorTable from '@/components/operators/OperatorTable.vue';
import RevokeOperatorDialog from '@/components/operators/RevokeOperatorDialog.vue';
import { useOperatorPolling } from '@/composables/useOperatorPolling';
import { useAuthStore } from '@/stores/auth';
import { useOperatorStore } from '@/stores/operator';
import type { OperatorListFilters, OperatorSummary } from '@/types/operator';

const route = useRoute();
const router = useRouter();
const store = useOperatorStore();
const auth = useAuthStore();
const customerId = computed(() => String(route.params.customerId));
const { heartbeatIntervalSeconds } = storeToRefs(store);
const clusterId = computed(() => String(route.params.clusterId));
const selectedForRevoke = shallowRef<OperatorSummary | null>(null);

async function refresh(): Promise<boolean> {
  return store.loadList(customerId.value, clusterId.value);
}

async function handleFilters(filters: OperatorListFilters): Promise<void> {
  store.setFilters(filters);
  await refresh();
}

async function confirmRevoke(reason: string): Promise<void> {
  if (!selectedForRevoke.value) return;
  if (await store.revokeOperator(customerId.value, clusterId.value, selectedForRevoke.value.id, reason)) {
    selectedForRevoke.value = null;
  }
}

watch([customerId, clusterId], async () => {
  selectedForRevoke.value = null;
  store.resetListState();
  await refresh();
}, { immediate: true });
useOperatorPolling({ heartbeatIntervalSeconds, refresh });
</script>

<template>
  <section class="page">
    <header class="page__header">
      <div>
        <p class="eyebrow">{{ t('operator.list.title') }}</p>
        <h1>{{ t('operator.list.subtitle') }}</h1>
        <p>{{ t('operator.list.description') }}</p>
      </div>
      <div class="actions">
        <button type="button" @click="refresh">{{ t('action.refresh') }}</button>
        <RouterLink
          v-if="auth.canEnrollOperators"
          class="primary"
          :to="{ name: 'OperatorEnroll', params: { customerId: customerId, clusterId: clusterId } }"
        >{{ t('operator.list.generate') }}</RouterLink>
      </div>
    </header>

    <LoadingState v-if="store.loading && !store.hasOperators" :message="t('operator.list.loading')" />
    <ForbiddenState v-else-if="store.forbidden" />
    <ErrorState v-else-if="store.error && !store.hasOperators" :message="store.error.message">
      <button type="button" @click="refresh">{{ t('action.retry') }}</button>
    </ErrorState>
    <EmptyState
      v-else-if="!store.hasOperators"
      :title="t('operator.list.empty')"
      :message="t('operator.list.emptyHint')"
    >
      <template #action>
        <RouterLink
          v-if="auth.canEnrollOperators"
          class="primary"
          :to="{ name: 'OperatorEnroll', params: { customerId: customerId, clusterId: clusterId } }"
        >{{ t('operator.list.generateFirst') }}</RouterLink>
      </template>
    </EmptyState>

    <div v-else class="content">
      <p v-if="store.error" class="warning" role="alert">
        {{ store.error.message }} {{ t('operator.list.stale') }}
      </p>
      <OperatorFilters :model-value="store.filters" @update:model-value="handleFilters" />
      <OperatorTable
        :operators="store.operators"
        :can-revoke="auth.canRevokeOperators"
        @open="router.push({ name: 'OperatorDetail', params: { customerId: customerId, clusterId: clusterId, operatorId: $event } })"
        @revoke="selectedForRevoke = $event"
      />
      <button
        v-if="store.nextPageToken"
        type="button"
        :disabled="store.loadingMore"
        @click="store.loadList(customerId, clusterId, true)"
      >{{ store.loadingMore ? t('state.loading.label') : t('operator.list.loadMore') }}</button>
    </div>

    <RevokeOperatorDialog
      v-if="selectedForRevoke"
      :operator-name="selectedForRevoke.name"
      :submitting="store.saving"
      :error-message="store.error?.message"
      @cancel="selectedForRevoke = null"
      @confirm="confirmRevoke"
    />
  </section>
</template>

<style scoped>
.page { display: grid; gap: 1.5rem; max-width: 76rem; margin: 0 auto; }
.page__header, .actions { display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
h1, p { margin: 0; }
.page__header > div:first-child > p:last-child { margin-top: 0.375rem; color: var(--color-muted); }
.eyebrow { color: var(--color-primary); font-size: var(--font-size-xs); font-weight: 800; text-transform: uppercase; }
.actions { justify-content: flex-end; }
.actions button, .primary, .content > button { padding: 0.6rem 0.85rem; border: 1px solid var(--color-subtle); border-radius: 0.375rem; background: var(--color-surface); color: inherit; cursor: pointer; text-decoration: none; }
.primary { border-color: var(--color-primary); background: var(--color-primary); color: var(--color-on-accent); }
.content { display: grid; gap: 1rem; }
.warning { padding: 0.75rem; border-radius: 0.5rem; background: var(--color-warning-soft); color: var(--color-warning-ink); }
</style>
