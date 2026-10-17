<script setup lang="ts">
import { statusLabel } from '@/i18n/status-labels';
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { t } from '@/i18n/messages';
import { useRoute, useRouter } from 'vue-router';
import CancelOperationDialog from '@/components/operations/CancelOperationDialog.vue';
import DisconnectBanner from '@/components/operations/DisconnectBanner.vue';
import OperationTimeline from '@/components/operations/OperationTimeline.vue';
import PreflightResultPanel from '@/components/operations/PreflightResultPanel.vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import EmergencyResultPanel from '@/components/emergency/EmergencyResultPanel.vue';
import { getDefinition } from '@/connect/definition-api';
import { getEmergencyResult } from '@/connect/emergency-api';
import { correlationLine, describeError } from '@/connect/error-copy';
import { getPreflightResult } from '@/connect/operation-api';
import { useEmergencyEffectObservation } from '@/composables/useEmergencyEffectObservation';
import { useAuthStore } from '@/stores/auth';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import { useOperationTimelineStore } from '@/stores/operationTimeline';
import type { EmergencyResultDisplay } from '@/features/emergency/model';
import type { PreflightResult } from '@/types/operation';

const route = useRoute();
const router = useRouter();
const store = useOperationTimelineStore();
const authStore = useAuthStore();
const operationId = computed(() => String(route.params.operationId ?? ''));
const releaseName = computed(() => String(route.query.releaseName ?? route.params.releaseId ?? 'Release'));
/*
 * The release context this page addresses (TASK-279). The canonical nested route
 * carries customer/cluster/release in the URL; the Operation centre's scope-less
 * route (`/operations/:operationId`, TASK-277) carries only the operation id, and
 * vue-router refuses a named route whose required params are empty. Instead of
 * masking every release-scoped action there, the page recovers the context from the
 * operation's own release definition — the same release:read the operation stream
 * already needs. The operation itself renders while that read is in flight or
 * refused; only the actions that need the context wait for it.
 */
type ReleaseScope = { customerId: string; clusterId: string; releaseId: string };

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

const resolvedScope = ref<ReleaseScope | null>(null);
const releaseScope = computed(() => routeReleaseScope() ?? resolvedScope.value);

// Full route scope: a same operationId under a different customer/cluster/
// release must reset the store and open a fresh stream (AC-057-15).
const routeScope = computed(() =>
  [route.params.customerId, route.params.clusterId, route.params.releaseId, route.params.operationId]
    .map(String)
    .join('/'),
);

/**
 * Recovers the release context from the operation's release definition when the URL
 * does not carry it. Failures are deliberately silent here: the page's own error
 * surface belongs to the operation stream, and a refused definition read must leave
 * the operation readable rather than replace it with an error state.
 */
async function resolveReleaseScope(definitionId: string): Promise<void> {
  if (!releaseScopedRoutes) return;
  const requestedOperationId = operationId.value;
  /*
   * The route param alone is not "the operation on screen": across a same-id
   * scope switch it stays the same while the store is reset (operationId null)
   * and reloaded. The store's own identity is the authority for which operation
   * this definition read belongs to; `routeReleaseScope()` still stops a route
   * that has since gained its own scope from being overwritten. TASK-286.
   */
  const stillCurrent = (): boolean =>
    operationId.value === requestedOperationId &&
    !routeReleaseScope() &&
    store.operationId === requestedOperationId &&
    store.operation?.releaseDefinitionId === definitionId;
  try {
    const definition = await getDefinition(definitionId);
    // A late response for a previous operation must not overwrite the context
    // on screen.
    if (!stillCurrent()) return;
    resolvedScope.value =
      definition.customerId && definition.clusterId
        ? { customerId: definition.customerId, clusterId: definition.clusterId, releaseId: definitionId }
        : null;
  } catch {
    if (!stillCurrent()) return;
    resolvedScope.value = null;
  }
}

watch(
  () => [routeScope.value, store.operation?.releaseDefinitionId] as const,
  () => {
    if (routeReleaseScope()) {
      resolvedScope.value = null;
      return;
    }
    const definitionId = store.operation?.releaseDefinitionId ?? '';
    if (!definitionId) {
      resolvedScope.value = null;
      return;
    }
    void resolveReleaseScope(definitionId);
  },
  { immediate: true },
);

const liveUpdatesEnabled = import.meta.env.VITE_OPERATION_LIVE_UPDATES !== 'false';

// ── Preflight stage results (TASK-149 / AC-056-03) ─────────────────────────
// The preflight outcome is part of GetOperation, not of the stream snapshot, so
// it is fetched alongside the stream and re-fetched when the operation moves on
// (the coordinator persists it as preflight concludes).
const preflightResult = ref<PreflightResult | null>(null);
/*
 * TASK-281: a failed preflight read is NOT the same as "the operation returned no
 * preflight result yet". Keeping the failure in its own state makes the two
 * distinguishable, gives the operator a retry entry point, and carries the
 * correlation line (code · requestId) so a failure can be handed to the server logs.
 */
const preflightFailure = ref<{ message: string; details: string } | null>(null);

async function loadPreflightResult(): Promise<void> {
  const current = operationId.value;
  if (!current) {
    preflightResult.value = null;
    preflightFailure.value = null;
    return;
  }
  try {
    const result = await getPreflightResult(current);
    // A late response for a previous operation must not overwrite the current
    // one (same guard the store applies to its own async reads).
    if (operationId.value === current) {
      preflightResult.value = result;
      preflightFailure.value = null;
    }
  } catch (error) {
    // The operation and its last_error still render, but the read failure is
    // surfaced rather than swallowed: message plus the correlation line.
    if (operationId.value === current) {
      preflightResult.value = null;
      preflightFailure.value = {
        message: t('operation.preflight.loadFailedHint'),
        details: correlationLine(describeError(error)),
      };
    }
  }
}

// Configure store-level seams once (production defaults, no-ops).
store.configure({ liveUpdatesEnabled: () => liveUpdatesEnabled });

// ── Emergency result observation (TASK-058 Step 5 seam) ────────────────────
// The EmergencyResultPanel restores the full result from GetOperation alone
// (AC-058-20) and re-fetches whenever the authoritative stateVersion moves
// (late Result resolution increments it — AC-058-22). UNKNOWN observation
// reuses the operationTimeline watch lifecycle (D3/D10).
const authorization = useEmergencyAuthorizationStore();
const emergencyResult = ref<EmergencyResultDisplay | null>(null);
const effectObservation = useEmergencyEffectObservation();

const isEmergency = computed(() => store.operation?.operationType === 'EMERGENCY');

async function loadEmergencyResult(): Promise<void> {
  const currentId = operationId.value;
  if (!currentId || !isEmergency.value) return;
  try {
    const result = await getEmergencyResult(currentId);
    if (operationId.value !== currentId) return;
    emergencyResult.value = result;
  } catch {
    // The summary/timeline already surface stream errors; the panel simply
    // keeps the last authoritative result when the fetch fails transiently.
  }
}

watch(
  () => [operationId.value, isEmergency.value],
  () => {
    emergencyResult.value = null;
    if (isEmergency.value) {
      void loadEmergencyResult();
      effectObservation.start(operationId.value, () => liveUpdatesEnabled);
    } else {
      effectObservation.stop();
    }
  },
  { immediate: true },
);

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

watch(
  () => store.operation?.stateVersion,
  (current, previous) => {
    if (isEmergency.value && current !== previous) {
      void loadEmergencyResult();
    }
  },
);

const cancelTrigger = ref<HTMLButtonElement | null>(null);
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
    effectObservation.stop();
    store.reset();
    // The preflight read belongs to the scope being left. Clearing both halves
    // synchronously stops the previous operation's failure banner (or result
    // panel) from rendering while the new scope's read is still in flight;
    // loadPreflightResult() below repopulates them from the new scope.
    preflightResult.value = null;
    preflightFailure.value = null;
  }
  void store.load(nextOperationId);
  void loadPreflightResult();
}, { immediate: true });

// The coordinator persists the preflight result as the pipeline concludes, so
// re-read it whenever the operation moves on (AC-056-03).
watch(
  () => [operationId.value, store.operation?.stateVersion] as const,
  () => void loadPreflightResult(),
);
onBeforeUnmount(() => {
  effectObservation.stop();
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

function formatTimestamp(value: string | null): string {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' }).format(new Date(value)) : '—';
}
</script>

<template>
  <section class="operation-detail">
    <nav class="operation-detail__breadcrumbs" :aria-label="t('values.page.breadcrumb')">
      <template v-if="releaseScope">
        <RouterLink
          :to="{
            name: 'ReleaseInventory',
            params: { customerId: releaseScope.customerId, clusterId: releaseScope.clusterId },
          }"
        >
          Releases
        </RouterLink>
        <span aria-hidden="true">/</span>
        <RouterLink
          :to="{
            name: 'OperationList',
            params: {
              customerId: releaseScope.customerId,
              clusterId: releaseScope.clusterId,
              releaseId: releaseScope.releaseId,
            },
          }"
        >
          操作历史
        </RouterLink>
        <span aria-hidden="true">/</span>
      </template>
      <template v-else>
        <RouterLink :to="{ name: 'OperationCenter' }">{{ t('operationCenter.title') }}</RouterLink>
        <span aria-hidden="true">/</span>
      </template>
      <span>{{ releaseName }}</span><span aria-hidden="true">/</span>
      <strong>{{ operationId }}</strong>
    </nav>

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

      <header class="operation-detail__header">
        <div>
          <p class="operation-detail__eyebrow">{{ statusLabel('operationType', store.operation?.operationType) }}操作</p>
          <h1>{{ releaseName }}</h1>
          <p><code>{{ store.operation?.operationId }}</code></p>
        </div>
        <div class="operation-detail__header-actions">
          <span v-if="store.operation" class="operation-detail__state" :class="`operation-detail__state--${store.operation.state}`">
            {{ statusLabel('operation', store.operation.state) }}
          </span>
          <template v-if="store.showCancel">
            <button
              v-if="store.canCancel"
              ref="cancelTrigger"
              type="button"
              class="operation-detail__cancel"
              :disabled="store.cancelLoading"
              @click="store.cancelDialogOpen = true"
            >
              {{ store.cancelLoading ? '取消中…' : '取消操作' }}
            </button>
            <button
              v-else-if="store.operation?.state === 'cancelling'"
              type="button"
              class="operation-detail__cancel operation-detail__cancel--disabled"
              disabled
            >
              取消中…
            </button>
            <button
              v-else-if="store.isTerminal"
              type="button"
              class="operation-detail__cancel operation-detail__cancel--disabled"
              disabled
              title="操作已完成，无法取消"
            >
              取消操作
            </button>
          </template>
          <button
            v-if="store.operation"
            type="button"
            class="operation-detail__refresh"
            @click="() => void store.refresh()"
          >
            刷新
          </button>
        </div>
      </header>

      <DisconnectBanner :visible="store.streamStatus === 'disconnected'" />

      <dl class="operation-detail__summary">
        <div><dt>ReleaseDefinition</dt><dd>{{ store.operation?.releaseDefinitionId }}</dd></div>
        <div><dt>StateVersion</dt><dd>{{ store.operation?.stateVersion.toString() }}</dd></div>
        <div v-if="store.operation?.operationType === 'EMERGENCY'">
          <dt>{{ t('emergency.result.effectStatus') }}</dt><dd>{{ statusLabel('effect', store.operation?.effectStatus ?? undefined) }}</dd>
        </div>
        <div><dt>TargetRevision</dt><dd>{{ store.operation?.targetRevision || '—' }}</dd></div>
        <div><dt>创建时间</dt><dd>{{ formatTimestamp(store.operation?.createdAt ?? null) }}</dd></div>
        <div><dt>更新时间</dt><dd>{{ formatTimestamp(store.operation?.updatedAt ?? null) }}</dd></div>
        <div><dt>终止时间</dt><dd>{{ formatTimestamp(store.operation?.terminalAt ?? null) }}</dd></div>
      </dl>

      <EmergencyResultPanel
        v-if="isEmergency"
        :result="emergencyResult"
        :operation-state="store.operation?.state ?? ''"
        :operation-effect-status="store.operation?.effectStatus ?? ''"
        :observation-status="effectObservation.status.value"
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
.operation-detail { display: grid; gap: 1.5rem; max-width: 70rem; margin: 0 auto; }
.operation-detail__breadcrumbs { display: flex; flex-wrap: wrap; gap: 0.45rem; color: var(--color-muted); font-size: var(--font-size-sm); }
.operation-detail__breadcrumbs a { color: var(--color-primary); }
.operation-detail__header { display: flex; justify-content: space-between; align-items: flex-start; gap: 1rem; }
.operation-detail__header h1, .operation-detail__header p { margin: 0; }
.operation-detail__eyebrow { color: var(--color-primary); font-size: var(--font-size-xs); font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.operation-detail__state { padding: 0.45rem 0.7rem; border-radius: 999px; background: var(--color-border); text-transform: uppercase; font-size: var(--font-size-xs); font-weight: 800; }
.operation-detail__state--failed, .operation-detail__state--timeout { background: var(--color-danger-surface); color: var(--color-error); }
.operation-detail__state--succeeded { background: var(--color-success-surface); color: var(--color-success-ink); }
.operation-detail__header-actions { display: flex; align-items: center; gap: 0.75rem; }
.operation-detail__cancel { min-height: 2.4rem; padding: 0.45rem 0.85rem; border: 1px solid var(--color-danger); border-radius: 0.45rem; background: var(--color-surface); color: var(--color-danger); cursor: pointer; font-weight: 700; }
.operation-detail__cancel--disabled { border-color: var(--color-border-strong); color: var(--color-subtle); cursor: not-allowed; }
.operation-detail__refresh { min-height: 2.4rem; padding: 0.45rem 0.85rem; border: 1px solid var(--color-border-strong); border-radius: 0.45rem; background: var(--color-surface); color: var(--color-text-secondary); cursor: pointer; font-weight: 600; }
.operation-detail__summary { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); margin: 0; border: 1px solid var(--color-border-strong); border-radius: 0.7rem; background: var(--color-surface); }
.operation-detail__summary div { padding: 1rem; border-bottom: 1px solid var(--color-border); }
.operation-detail__summary dt { color: var(--color-muted); font-size: var(--font-size-sm); }
.operation-detail__summary dd { margin: 0.25rem 0 0; font-weight: 650; overflow-wrap: anywhere; }
@media (max-width: 52rem) { .operation-detail__summary { grid-template-columns: 1fr; } }
</style>
