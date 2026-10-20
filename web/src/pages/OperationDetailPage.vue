<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import type { RouteLocationRaw } from 'vue-router';
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
import { useRoute, useRouter } from 'vue-router';
import CancelOperationDialog from '@/components/operations/CancelOperationDialog.vue';
import DisconnectBanner from '@/components/operations/DisconnectBanner.vue';
import OperationDetailHeader from '@/components/operations/OperationDetailHeader.vue';
import OperationDetailSummary from '@/components/operations/OperationDetailSummary.vue';
import OperationTimeline from '@/components/operations/OperationTimeline.vue';
import PreflightResultPanel from '@/components/operations/PreflightResultPanel.vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import Steps from '@/components/common/Steps.vue';
import EmergencyResultPanel from '@/components/emergency/EmergencyResultPanel.vue';
import { getOperation, mapOperationError } from '@/connect/operation-api';
import { useOperationEmergencyResult } from '@/composables/useOperationEmergencyResult';
import { useOperationPreflight } from '@/composables/useOperationPreflight';
import { useOperationReleaseScope, type ReleaseScope } from '@/composables/useOperationReleaseScope';
import { useScopeNames } from '@/composables/useScopeNames';
import { useAuthStore } from '@/stores/auth';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import { useOperationTimelineStore } from '@/stores/operationTimeline';

const route = useRoute();
const router = useRouter();
const store = useOperationTimelineStore();
const authStore = useAuthStore();
const operationId = computed(() => String(route.params.operationId ?? ''));

/*
 * NAMES, never identifiers (TASK-394). The detail page used to title itself with the
 * release argument falling back to the release DEFINITION id, and its breadcrumb ended
 * with the raw operation id — two UUIDs on screen. The release name comes from the
 * query (every in-app link carries it; a direct URL may not, in which case the level is
 * dropped). The customer/cluster names come from the same resolver the release
 * inventory and create pages use, except that this page does not fall back to the
 * identifier: a missing name removes the level.
 */
const { customerId, clusterId, customerName, clusterName } = useScopeNames();
const releaseName = computed(() =>
  typeof route.query.releaseName === 'string' ? route.query.releaseName : '',
);
const customerLabel = computed(() =>
  customerName.value && customerName.value !== customerId.value ? customerName.value : '',
);
const clusterLabel = computed(() =>
  clusterName.value && clusterName.value !== clusterId.value ? clusterName.value : '',
);

// The release-scoped routes register together (router/index.ts). Where they are
// absent (release inventory disabled) there is no scoped page to navigate into, so
// a resolved definition must not flip this page into scoped rendering.
const releaseScopedRoutes =
  router.hasRoute('ReleaseInventory') && router.hasRoute('OperationList') && router.hasRoute('ConvergenceTasks');

function routeReleaseScope(): ReleaseScope | null {
  const customer = String(route.params.customerId ?? '');
  const cluster = String(route.params.clusterId ?? '');
  const release = String(route.params.releaseId ?? '');
  return customer && cluster && release ? { customerId: customer, clusterId: cluster, releaseId: release } : null;
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

/* ---------------------------------------------------------------------------------
 * Heading, breadcrumb and lifecycle steps (TASK-394 direction C)
 * --------------------------------------------------------------------------------- */

const operationListRoute = computed<RouteLocationRaw | null>(() => {
  const scope = releaseScope.value;
  // A route registration is not reactive, but it is fixed before this page mounts.
  if (!scope || !router.hasRoute('OperationList')) return null;
  return { name: 'OperationList', params: { ...scope } };
});

/*
 * Breadcrumb links preserve the trail the old hand-rolled nav offered (Releases /
 * operation history), but each one is built from the scope and only rendered when its
 * route is registered. Labels stay names — a level without a name is a plain label, never
 * an identifier.
 */
const customerRoute = computed<RouteLocationRaw | null>(() => {
  if (!customerId.value || !router.hasRoute('ClusterList')) return null;
  return { name: 'ClusterList', params: { customerId: customerId.value } };
});

const clusterRoute = computed<RouteLocationRaw | null>(() => {
  if (!customerId.value || !clusterId.value || !router.hasRoute('ReleaseInventory')) return null;
  return { name: 'ReleaseInventory', params: { customerId: customerId.value, clusterId: clusterId.value } };
});

const pageTitle = computed(() => {
  const operation = store.operation;
  if (!operation) return t('operation.detail.breadcrumb');
  const type = statusLabel('operationType', operation.operationType);
  return releaseName.value
    ? t('operation.detail.titleWithRelease', { type, release: releaseName.value })
    : t('operation.detail.title', { type });
});

const pageSubtitle = computed(() => {
  const operation = store.operation;
  if (!operation) return '';
  return t('operation.detail.subtitle', {
    state: statusLabel('operation', operation.state),
    version: operation.stateVersion.toString(),
  });
});

const crumbs = computed<Array<{ label: string; to?: RouteLocationRaw }>>(() => {
  const trail: Array<{ label: string; to?: RouteLocationRaw }> = [];
  if (customerLabel.value) trail.push({ label: customerLabel.value, to: customerRoute.value ?? undefined });
  if (clusterLabel.value) trail.push({ label: clusterLabel.value, to: clusterRoute.value ?? undefined });
  if (releaseName.value) {
    trail.push({ label: releaseName.value, to: operationListRoute.value ?? undefined });
  }
  // The scope-less Operation-centre entry has no scope name to anchor on, so the
  // trail starts at the centre rather than at an unnameable identifier.
  if (!customerId.value && router.hasRoute('OperationCenter')) {
    trail.unshift({ label: t('operationCenter.title'), to: { name: 'OperationCenter' } });
  }
  trail.push({ label: t('operation.detail.breadcrumb') });
  return trail;
});

/*
 * Step states are DERIVED from the operation state machine, never hard-coded:
 * internal/orchestrator/operation/state.go:15-30 (events), :35-62 (transitions) and
 * :78-89 (EMERGENCY skips preflight). The four console phases map onto the real
 * lifecycle as follows:
 *   preflight    pending / preflight           — the preflight pipeline is running
 *   approval     the admission gate that lets a queued operation exist: an approved
 *                ValuesRevision is required before CreateOperation
 *                (internal/orchestrator/service.go:451-452, :1580) and preflight must
 *                pass to reach `queued` (state.go:44-47); an EMERGENCY is admitted by
 *                the emergency authorization (pending→queued, no preflight)
 *   dispatch     queued / running / cancelling — handed to the operator and executing
 *   convergence  the terminal row, plus the EMERGENCY effect observation while the
 *                cluster effect is still UNKNOWN (store.emergencyEffectStatus)
 * A step before the current phase is `done`, the current one is `now`, the rest
 * `todo`. EMERGENCY never runs preflight, so that step is vacuously complete for it
 * (the same rule the create page applies to a rollback's absent patch step).
 */
const PREFLIGHT_PHASE = 0;
const APPROVAL_PHASE = 1;
const DISPATCH_PHASE = 2;
const CONVERGENCE_PHASE = 3;

const phaseIndex = computed<number>(() => {
  const operation = store.operation;
  if (!operation) return PREFLIGHT_PHASE;
  switch (operation.state) {
    case 'pending':
      return operation.operationType === 'EMERGENCY' ? APPROVAL_PHASE : PREFLIGHT_PHASE;
    case 'preflight':
      return PREFLIGHT_PHASE;
    case 'queued':
    case 'running':
    case 'cancelling':
      return DISPATCH_PHASE;
    default:
      // succeeded / failed / cancelled / timeout: the operation has converged.
      return CONVERGENCE_PHASE;
  }
});

const steps = computed<Array<{ label: string; state: 'done' | 'now' | 'todo' }>>(() => {
  const labels = [
    t('operation.detail.steps.preflight'),
    t('operation.detail.steps.approval'),
    t('operation.detail.steps.dispatch'),
    t('operation.detail.steps.convergence'),
  ];
  const index = phaseIndex.value;
  // A terminal EMERGENCY whose effect is still UNKNOWN keeps the last step current:
  // the convergence task (or the effect confirmation) has not resolved yet (AC-057-19).
  const awaitingEffect =
    store.isTerminal &&
    store.operation?.operationType === 'EMERGENCY' &&
    store.operation.effectStatus === 'UNKNOWN' &&
    store.emergencyEffectStatus === 'watching';
  return labels.map((label, position) => ({
    label,
    state:
      position < index || (position === index && store.isTerminal && !awaitingEffect)
        ? 'done'
        : position === index
          ? 'now'
          : 'todo',
  }));
});

/* ---------------------------------------------------------------------------------
 * Load failure surface (TASK-394)
 *
 * The store treats a stream that fails AFTER it opened as a disconnect: it keeps the
 * reconnect/poll loop alive and sets neither `operation` nor `initialError`. An
 * unknown operation id therefore rendered the loading state forever (production:
 * placeholder id → no error, no 404). The page cannot change the store, so it does
 * two things itself:
 *   1. on the first `disconnected` transition with no data it re-reads the operation
 *      once; a failure there becomes the error state (NOT_FOUND/PERMISSION_DENIED are
 *      terminal, the rest keep the retry entry), a success is pulled into the store;
 *   2. a deadline bounds the initial load even when the server never answers.
 * --------------------------------------------------------------------------------- */
const LOAD_DEADLINE_MS = 15_000;
const loadProbe = ref<{ code: string; message: string; retryable: boolean } | null>(null);
let loadDeadline: ReturnType<typeof setTimeout> | null = null;
let probedOperationId: string | null = null;

const loadFailure = computed(() => store.initialError ?? loadProbe.value);
// The live-updates-off deployment has its own explicit-refresh state below, so the
// spinner is only ever the live mode's "waiting for the first snapshot".
const loading = computed(() => !store.operation && !loadFailure.value && liveUpdatesEnabled);

const loadFailureMessage = computed(() => {
  const failure = loadFailure.value;
  if (!failure) return '';
  return `${failure.message}（${t('operation.detail.errorCode')}：${failure.code}）`;
});

const backTarget = computed<RouteLocationRaw | null>(() => {
  if (operationListRoute.value) return operationListRoute.value;
  if (router.hasRoute('OperationCenter')) return { name: 'OperationCenter' };
  return null;
});

const backLabel = computed(() =>
  operationListRoute.value ? t('operation.detail.backToHistory') : t('operation.detail.backToCenter'),
);

function clearLoadDeadline(): void {
  if (loadDeadline !== null) {
    clearTimeout(loadDeadline);
    loadDeadline = null;
  }
}

function armLoadDeadline(): void {
  clearLoadDeadline();
  if (!liveUpdatesEnabled || !operationId.value) return;
  loadDeadline = setTimeout(() => {
    loadDeadline = null;
    if (store.operation || loadFailure.value) return;
    loadProbe.value = {
      code: 'load_timeout',
      message: t('operation.detail.loadTimeout'),
      retryable: true,
    };
  }, LOAD_DEADLINE_MS);
}

async function classifyMissingOperation(currentId: string): Promise<void> {
  if (!currentId || probedOperationId === currentId) return;
  probedOperationId = currentId;
  try {
    await getOperation(currentId);
    // The operation exists; the stream — not the operation — is what failed. Pull the
    // authoritative row in so the page is usable while the store reconnects.
    if (operationId.value === currentId) await store.refresh();
  } catch (error) {
    if (operationId.value !== currentId) return;
    const mapped = mapOperationError(error);
    loadProbe.value = { code: mapped.code, message: mapped.message, retryable: mapped.retryable };
    clearLoadDeadline();
  }
}

function retryLoad(): void {
  loadProbe.value = null;
  probedOperationId = null;
  store.retryInitial();
  armLoadDeadline();
}

watch(
  () => [store.operation, store.initialError, store.streamStatus] as const,
  ([operation, initialError, status]) => {
    if (operation) {
      clearLoadDeadline();
      loadProbe.value = null;
      return;
    }
    if (initialError) {
      clearLoadDeadline();
      return;
    }
    if (status === 'disconnected') void classifyMissingOperation(operationId.value);
  },
  { immediate: true },
);

watch(
  operationId,
  (current) => {
    loadProbe.value = null;
    probedOperationId = null;
    if (current) armLoadDeadline();
    else clearLoadDeadline();
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
  clearLoadDeadline();
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
    <PageHeader :title="pageTitle" :subtitle="pageSubtitle" :crumbs="crumbs">
      <template #actions>
        <OperationDetailHeader
          v-if="store.operation"
          :operation="store.operation"
          :show-cancel="store.showCancel"
          :can-cancel="store.canCancel"
          :cancel-loading="store.cancelLoading"
          :is-terminal="store.isTerminal"
          @request-cancel="store.cancelDialogOpen = true"
          @refresh="() => void store.refresh()"
        />
      </template>
    </PageHeader>

    <LoadingState v-if="loading" :message="t('operation.detail.loading')" />
    <EmptyState
      v-else-if="!store.operation && !loadFailure"
      :title="t('operation.detail.liveUpdatesOff')"
      :message="t('operation.detail.liveUpdatesHint')"
      :action-label="t('action.refresh')"
      @action="() => void store.refresh()"
    />
    <ErrorState
      v-else-if="!store.operation && loadFailure"
      data-testid="operation-load-failure"
      :title="t('operation.detail.loadFailed')"
      :message="loadFailureMessage"
      :details="`code=${loadFailure.code}`"
    >
      <template #action>
        <div class="operation-detail__error-actions">
          <button
            v-if="loadFailure.retryable"
            type="button"
            class="operation-detail__error-retry"
            @click="retryLoad"
          >
            {{ t('action.retry') }}
          </button>
          <RouterLink v-if="backTarget" class="operation-detail__error-back" :to="backTarget">
            {{ backLabel }}
          </RouterLink>
        </div>
      </template>
    </ErrorState>
    <template v-else>
      <Steps class="operation-detail__steps" :steps="steps" />

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

      <div class="operation-detail__grid">
        <section class="operation-detail__pane" :aria-label="t('operation.detail.preflightPane')">
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
          <EmptyState
            v-if="!preflightFailure && !preflightResult"
            :title="t('operation.detail.preflightEmptyTitle')"
            :message="t('operation.detail.preflightEmptyMessage')"
          />
        </section>

        <section class="operation-detail__pane" :aria-label="t('operation.detail.timelinePane')">
          <OperationTimeline
            :entries="store.entries"
            :operation="store.operation"
            :stream-status="store.streamStatus"
            :history-truncated="store.historyTruncated"
            :history-gap="store.historyGap"
            :emergency-effect-status="store.emergencyEffectStatus"
          />
        </section>
      </div>

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

/* Direction C stepper strip: the row reads as one control above the two columns. */
.operation-detail__steps {
  padding: 1rem 1.25rem;
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

/* Two columns above 900px; a single column below (375 is the narrowest supported). */
.operation-detail__grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1.5rem; align-items: start; }
.operation-detail__pane { display: grid; gap: 1rem; min-width: 0; align-content: start; }
.operation-detail__pane > * { min-width: 0; }

.operation-detail__error-actions { display: flex; flex-wrap: wrap; gap: 0.75rem; align-items: center; justify-content: center; }
.operation-detail__error-retry { padding: 0.45rem 0.75rem; border: 1px solid var(--color-danger-border); border-radius: var(--radius-md); background: var(--color-surface); color: var(--color-error-strong); cursor: pointer; }
.operation-detail__error-back { color: var(--color-primary); }

@media (max-width: 56.25rem) {
  .operation-detail__grid { grid-template-columns: minmax(0, 1fr); }
}
</style>
