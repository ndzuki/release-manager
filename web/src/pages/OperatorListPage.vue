<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, ref, shallowRef, watch } from 'vue';
import { storeToRefs } from 'pinia';
import { useRoute, useRouter } from 'vue-router';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import Toast from '@/components/common/Toast.vue';
import OperatorFilters from '@/components/operators/OperatorFilters.vue';
import OperatorPollingNotice from '@/components/operators/OperatorPollingNotice.vue';
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
/*
 * W10 (UX-041): revoking is destructive and irreversible, and the list used to
 * simply re-render with a "revoked" badge. The toast names the operator so the
 * operator sees which identity the confirmation applied to.
 */
const revokeNotice = ref<string | null>(null);

async function refresh(): Promise<boolean> {
  return store.loadList(customerId.value, clusterId.value);
}

async function handleFilters(filters: OperatorListFilters): Promise<void> {
  store.setFilters(filters);
  await refresh();
}

async function confirmRevoke(reason: string): Promise<void> {
  const target = selectedForRevoke.value;
  if (!target) return;
  if (await store.revokeOperator(customerId.value, clusterId.value, target.id, reason)) {
    selectedForRevoke.value = null;
    revokeNotice.value = t('operator.revoke.success', { name: target.name || target.id });
  }
}

watch([customerId, clusterId], async () => {
  selectedForRevoke.value = null;
  store.resetListState();
  await refresh();
}, { immediate: true });
// TASK-325: the poll's stop state must reach the page, not just the timer.
const { stopped: pollingStopped, resume: resumePolling } = useOperatorPolling({ heartbeatIntervalSeconds, refresh });
</script>

<template>
  <section class="page">
    <Toast
      v-if="revokeNotice"
      tone="success"
      testid="operator-list-notice"
      @dismiss="revokeNotice = null"
    >{{ revokeNotice }}</Toast>

    <OperatorPollingNotice :stopped="pollingStopped" @resume="resumePolling" />

    <!--
      W2 (TASK-394): the hand-rolled header had the title and the subtitle in the wrong
      slots — the eyebrow carried the LIST NAME ("集群 Operator") and the h1 carried the
      descriptor ("Operator 注册历史"). PageHeader now takes the list name as title and
      that descriptor as subtitle; the sentence that explains the page keeps its own line
      because PageHeader has exactly one subtitle. The category eyebrow is deliberately
      absent here: the only eyebrow string this page ever had is the title above.
    -->
    <PageHeader
      :title="t('operator.list.title')"
      :subtitle="t('operator.list.subtitle')"
    >
      <template #actions>
        <div class="actions">
          <button type="button" @click="refresh">{{ t('action.refresh') }}</button>
          <RouterLink
            v-if="auth.canEnrollOperators"
            class="primary"
            :to="{ name: 'OperatorEnroll', params: { customerId: customerId, clusterId: clusterId } }"
          >{{ t('operator.list.generate') }}</RouterLink>
        </div>
      </template>
    </PageHeader>

    <p class="page__intro">{{ t('operator.list.description') }}</p>

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
.page { display: grid; gap: 1.5rem; max-width: 76rem; margin: 0 auto; min-width: 0; }
/* PageHeader owns a 20px bottom margin; this page is a grid, so the gap above is the
   spacing between the header and the content and the margin must not stack on it. */
.page :deep(.page-header) { margin-bottom: 0; }
p { margin: 0; }
.page__intro { max-width: 72ch; color: var(--color-muted); }
.actions { display: flex; flex-wrap: wrap; justify-content: flex-end; align-items: center; gap: 1rem; }
.actions button, .primary, .content > button { padding: 0.6rem 0.85rem; border: 1px solid var(--color-border-strong); border-radius: 0.375rem; background: var(--color-surface); color: inherit; cursor: pointer; text-decoration: none; }
.primary { border-color: var(--color-primary); background: var(--color-primary); color: var(--color-on-accent); }
.content { display: grid; gap: 1rem; min-width: 0; }
.warning { padding: 0.75rem; border-radius: 0.5rem; background: var(--color-warning-soft); color: var(--color-warning-ink); }
</style>
