<script setup lang="ts">
import { computed, onBeforeUnmount, watch } from 'vue';
import { t } from '@/i18n/messages';
import { useRoute, useRouter } from 'vue-router';
import CancelOperationDialog from '@/components/operations/CancelOperationDialog.vue';
import DisconnectBanner from '@/components/operations/DisconnectBanner.vue';
import OperationDetailBreadcrumbs from '@/components/operations/OperationDetailBreadcrumbs.vue';
import OperationDetailHeader from '@/components/operations/OperationDetailHeader.vue';
import OperationDetailSummary from '@/components/operations/OperationDetailSummary.vue';
import OperationTimeline from '@/components/operations/OperationTimeline.vue';
import PreflightResultPanel from '@/components/operations/PreflightResultPanel.vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import EmergencyResultPanel from '@/components/emergency/EmergencyResultPanel.vue';
import { useOperationEmergencyResult } from '@/composables/useOperationEmergencyResult';
import { useOperationPreflight } from '@/composables/useOperationPreflight';
import { useOperationReleaseScope, type ReleaseScope } from '@/composables/useOperationReleaseScope';
import { useAuthStore } from '@/stores/auth';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import { useOperationTimelineStore } from '@/stores/operationTimeline';

const route = useRoute();
const router = useRouter();
const store = useOperationTimelineStore();
const authStore = useAuthStore();
const operationId = computed(() => String(route.params.operationId ?? ''));
const releaseName = computed(() => String(route.query.releaseName ?? route.params.releaseId ?? 'Release'));

// The release-scoped routes register together (router/index.ts). Where they are
// absent (release inventory disabled) there is no scoped page to navigate into, so
// a resolved definition must not flip this page into scoped rendering.
const releaseScopedRoutes =
  router.hasRoute('ReleaseInventory') && router.hasRoute('OperationList') && router.hasRoute('ConvergenceTasks');

function routeReleaseScope(): ReleaseScope | null {
  const customerId = String(route.params.customerId ?? '');
  const clusterId = String(route.params.clusterId ?? '');
  const releaseId = String(route.params.releaseId ?? '');
  return customerId && clusterId && releaseId ? { customerId, clusterId, releaseId } : null;
}

// Full route scope: a same operationId under a different customer/cluster/
// release must reset the store and open a fresh stream (AC-057-15).
const routeScope = computed(() =>
  [route.params.customerId, route.params.clusterId, route.params.releaseId, route.params.operationId]
    .map(String)
    .join('/'),
);

const { releaseScope } = useOperationReleaseScope({
  operationId: () => operationId.value,
  routeScope: routeReleaseScope,
  routeScopeKey: () => routeScope.value,
  scopedRoutesAvailable: () => releaseScopedRoutes,
  storeOperationId: () => store.operationId,
  storeDefinitionId: () => store.operation?.releaseDefinitionId ?? '',
});

const liveUpdatesEnabled = import.meta.env.VITE_OPERATION_LIVE_UPDATES !== 'false';

// Configure store-level seams once (production defaults, no-ops).
store.configure({ liveUpdatesEnabled: () => liveUpdatesEnabled });

const {
  result: preflightResult,
  failure: preflightFailure,
  reload: loadPreflightResult,
} = useOperationPreflight({
  operationId: () => operationId.value,
  scopeKey: () => routeScope.value,
  stateVersion: () => store.operation?.stateVersion,
});

const authorization = useEmergencyAuthorizationStore();
const isEmergency = computed(() => store.operation?.operationType === 'EMERGENCY');

const {
  result: emergencyResult,
  observationStatus,
  stopObservation,
} = useOperationEmergencyResult({
  operationId: () => operationId.value,
  isEmergency: () => isEmergency.value,
  stateVersion: () => store.operation?.stateVersion,
  liveUpdatesEnabled: () => liveUpdatesEnabled,
});

/*
 * The authorization snapshot is scoped to a customer, not to an operation. The
 * canonical route carries that customer in the URL; the scope-less route recovers
 * it from the release definition (TASK-279), so this follows the RESOLVED scope
 * rather than the raw route params — otherwise the snapshot would never load on
 * the scope-less entry and the capability projection would stay fail-closed.
 */
watch(
  () => [authStore.activeOrganization?.id ?? '', releaseScope.value?.customerId ?? ''] as const,
  ([organizationId, customerId]) => {
    if (organizationId && customerId) {
      void authorization.load(organizationId, customerId);
    }
  },
  { immediate: true },
);

let lastFocused: HTMLElement | null = null;
// a11y (grilling v15): return focus to the trigger when the dialog closes.
watch(
  () => store.cancelDialogOpen,
  (open, wasOpen) => {
    if (open) {
      lastFocused = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    } else if (wasOpen && lastFocused) {
      lastFocused.focus();
      lastFocused = null;
    }
  },
);

watch(routeScope, (current, previous) => {
  const nextOperationId = String(route.params.operationId ?? '');
  if (!nextOperationId) return;
  if (previous && current !== previous) {
    // Scope changed (possibly with the same operationId): reset first so the
    // load() early-return guard cannot absorb a same-id navigation
    // (AC-057-15: old stream/timers must stop before the new scope loads).
    //
    // TASK-286: tear the EMERGENCY effect observation down FIRST. stop() resets
    // the timeline store (its own scope generation++), and doing that after the
    // load() below discards the fresh stream — the page then hangs on
    // "正在加载 Operation…". The isEmergency watcher would otherwise call this
    // same stop() once the reset operation reads as non-EMERGENCY; after the
    // explicit teardown that second call is a no-op (currentOperationId is null),
    // so nothing invalidates the stream opened here.
    stopObservation();
    store.reset();
  }
  void store.load(nextOperationId);
}, { immediate: true });

onBeforeUnmount(() => {
  stopObservation();
  store.reset();
  authorization.reset();
});

function openConvergence(): void {
  // The canonical nested route (or a scope recovered from the operation's release
  // definition, TASK-279) is what makes the convergence route resolvable. Without
  // it there is nothing to push — vue-router would throw on the empty params.
  const scope = releaseScope.value;
  if (!scope) return;
  void router.push({
    name: 'ConvergenceTasks',
    params: { customerId: scope.customerId, clusterId: scope.clusterId, releaseId: scope.releaseId },
  });
}
</script>

<template>
  <section class="operation-detail">
    <OperationDetailBreadcrumbs
      :release-scope="releaseScope"
      :release-name="releaseName"
      :operation-id="operationId"
    />

    <LoadingState
      v-if="!store.operation && !store.initialError && liveUpdatesEnabled"
      message="正在加载 Operation…"
    />
    <EmptyState
      v-else-if="!store.operation && !store.initialError"
      title="实时更新已关闭"
      message="点击刷新加载 Operation 最新状态"
      action-label="刷新"
      @action="() => void store.refresh()"
    />
    <ErrorState
      v-else-if="!store.operation && store.initialError"
      title="Operation 加载失败"
      :message="store.initialError.message"
      :action-label="store.initialError.retryable ? '重试' : ''"
      @action="store.retryInitial"
    />
    <template v-else>
      <ErrorState
        v-if="preflightFailure"
        data-testid="preflight-load-failure"
        :title="t('operation.preflight.loadFailed')"
        :message="preflightFailure.message"
        :details="preflightFailure.details"
        :action-label="t('action.retry')"
        @action="loadPreflightResult"
      />
      <PreflightResultPanel v-else :result="preflightResult" />

      <OperationDetailHeader
        :operation="store.operation"
        :release-name="releaseName"
        :show-cancel="store.showCancel"
        :can-cancel="store.canCancel"
        :cancel-loading="store.cancelLoading"
        :is-terminal="store.isTerminal"
        @request-cancel="store.cancelDialogOpen = true"
        @refresh="() => void store.refresh()"
      />

      <DisconnectBanner :visible="store.streamStatus === 'disconnected'" />

      <OperationDetailSummary :operation="store.operation" />

      <EmergencyResultPanel
        v-if="isEmergency"
        :result="emergencyResult"
        :operation-state="store.operation?.state ?? ''"
        :operation-effect-status="store.operation?.effectStatus ?? ''"
        :observation-status="observationStatus"
        :can-create-values-revision="authorization.canCreateValuesRevision && releaseScope !== null"
        @open-convergence="openConvergence"
      />

      <OperationTimeline
        :entries="store.entries"
        :operation="store.operation"
        :stream-status="store.streamStatus"
        :history-truncated="store.historyTruncated"
        :history-gap="store.historyGap"
        :emergency-effect-status="store.emergencyEffectStatus"
      />

      <CancelOperationDialog
        v-if="store.cancelDialogOpen"
        :submitting="store.cancelLoading"
        :error="store.cancelError"
        :emergency-queued="store.operation?.operationType === 'EMERGENCY' && store.operation?.state === 'queued'"
        @submit="(reason) => void store.submitCancel(reason).then((result) => { if (result.ok) store.cancelDialogOpen = false; })"
        @close="store.cancelDialogOpen = false"
      />
    </template>
  </section>
</template>

<style scoped>
/* TASK-394: the page is one column, and an implicit `auto` track takes its MINIMUM from
   the widest child's min-content. The preflight stage detail is one unbreakable JSON
   "word" (~2030px in the production build), so the track grew to that and every sibling —
   including the 60-character disconnect banner — stretched with it: document.scrollWidth
   2190 at a 1440 viewport. `minmax(0, 1fr)` gives the track a zero minimum and
   `min-width: 0` removes the same automatic minimum from the items; the panel and the
   banner also break their own long strings. All three are required — the track rule alone
   still lets content paint outside an item's box. */
.operation-detail { display: grid; grid-template-columns: minmax(0, 1fr); gap: 1.5rem; max-width: 70rem; margin: 0 auto; }
.operation-detail > * { min-width: 0; }
</style>
