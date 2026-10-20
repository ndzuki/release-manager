<script setup lang="ts">
import { t } from '@/i18n/messages';
// Emergency change page (plan v3 Step 4): thin composition surface —
// scoped authorization gate → conflict check → target/action/form flow →
// frozen confirmation → Execute → Operation Detail. All business logic lives
// in the stores; this page only wires route scope, gates and navigation.
//
// The action selector picks ONE of image / replicas / annotations
// (ExecuteEmergencyChange carries a single action — REQ-081), and the matching
// payload control is mounted for the selected action. The replicas and
// annotation payloads reach the request through the store, never through a
// page-local copy of the wire shape.
import { computed, onBeforeUnmount, watch } from 'vue';
import { useRoute, useRouter, type RouteLocationRaw } from 'vue-router';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import AuthorizationStaleNotice from '@/components/common/AuthorizationStaleNotice.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import EmergencyActionSelector from '@/components/emergency/EmergencyActionSelector.vue';
import EmergencyAnnotationEditor from '@/components/emergency/EmergencyAnnotationEditor.vue';
import EmergencyArtifactSelector from '@/components/emergency/EmergencyArtifactSelector.vue';
import EmergencyChangeForm from '@/components/emergency/EmergencyChangeForm.vue';
import EmergencyConfirmDialog from '@/components/emergency/EmergencyConfirmDialog.vue';
import EmergencyReplicasInput from '@/components/emergency/EmergencyReplicasInput.vue';
import EmergencyTargetSelector from '@/components/emergency/EmergencyTargetSelector.vue';
import { useAuthStore } from '@/stores/auth';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import { useEmergencyChangeStore } from '@/stores/emergencyChange';
import type { ConvergencePolicy } from '@/features/emergency/model';

const route = useRoute();
const router = useRouter();
const authStore = useAuthStore();
const authorization = useEmergencyAuthorizationStore();
const store = useEmergencyChangeStore();

const releaseDefinitionId = computed(() => String(route.params.releaseId ?? ''));
const customerId = computed(() => String(route.params.customerId ?? ''));
const organizationId = computed(() => authStore.activeOrganization?.id ?? '');

const routeScope = computed(() =>
  [route.params.customerId, route.params.clusterId, route.params.releaseId].map(String).join('/'),
);

/*
 * TASK-394 (consistency wave 7): the hand-rolled `<nav class="breadcrumbs">` became
 * PageHeader's `crumbs`. The trail is built inside a computed so the release-inventory
 * target follows the route params instead of being captured once at setup. Crumb text is a
 * NAME only — PageHeader renders the last entry as the current page and never prints a
 * route identifier.
 */
const crumbs = computed<Array<{ label: string; to?: RouteLocationRaw }>>(() => [
  {
    label: t('cluster.detail.releases'),
    to: {
      name: 'ReleaseInventory',
      params: { customerId: customerId.value, clusterId: route.params.clusterId },
    },
  },
  { label: t('emergency.page.title') },
]);

const gate = computed(() => authorization.gateFor('execute'));
// AC-033-10: a stale authorization snapshot closes the NEW emergency entry. The
// convergence and existing-operation paths stay readable, so this only gates the
// write path — the server still rejects a call that ignores it.
const writeBlocked = computed(() => !authorization.writeAllowed);
const selectedTarget = computed(() => store.selectedTargetDisplay);

const submittingError = computed(() => store.submitError);

/** Whitelist keys for the selected annotation scope, in the target's order. */
const approvedAnnotationKeyNames = computed(() => store.approvedAnnotationKeys.map((entry) => entry.key));

/** The subset of those keys the read model has actually observed (TASK-274). */
const observedAnnotationKeyNames = computed(() => store.observedAnnotationKeys);

/**
 * Why the full definition whitelist is missing, when it is. The editor falls
 * back to the observed keys, and the operator has to know that the list is
 * shorter for a reason (TASK-274) rather than assume a key was never approved.
 */
const annotationWhitelistNotice = computed<string | null>(() => {
  if (store.annotationWhitelistState === 'violation') return t('annotation.whitelist.violation');
  if (store.annotationWhitelistState === 'unavailable') return t('annotation.whitelist.unavailable');
  return null;
});

/** Localized reason the replicas input is defensive-disabled (availability can
 * change when the target projection refreshes). */
const replicasUnavailableReason = computed(() => {
  if (store.replicasHpaManaged) return t('emergency.replicas.unavailable.hpa');
  return t('emergency.replicas.unavailable');
});

/** One-line summary of the action payload shown in the confirmation dialog
 * (null for image, whose container/artifact rows already carry it). */
const actionSummary = computed<string | null>(() => {
  if (store.actionKind === 'replicas') {
    return store.replicasValue === null
      ? null
      : t('emergency.confirm.action.replicas', { replicas: store.replicasValue });
  }
  if (store.actionKind === 'annotations') {
    const entries = store.annotationEntries.map((entry) => `${entry.key}=${entry.value}`).join('; ');
    return entries === '' ? null : t('emergency.confirm.action.annotations', { entries });
  }
  return null;
});

watch(routeScope, async (current, previous) => {
  if (previous && current !== previous) {
    store.reset();
    authorization.reset();
  }
  if (!organizationId.value || !customerId.value || !releaseDefinitionId.value) return;
  await authorization.load(organizationId.value, customerId.value);
  if (authorization.gateFor('execute') !== 'allowed') return;
  await store.loadScope({
    releaseDefinitionId: releaseDefinitionId.value,
    organizationId: organizationId.value,
    customerId: customerId.value,
  });
}, { immediate: true });

onBeforeUnmount(() => {
  store.reset();
  authorization.reset();
});

function operationRoute(operationId: string) {
  return {
    name: 'OperationDetail',
    params: {
      customerId: customerId.value,
      clusterId: String(route.params.clusterId ?? ''),
      releaseId: releaseDefinitionId.value,
      operationId,
    },
  };
}

function onSelectTarget(uid: string): void {
  const target = store.targets.find((candidate) => candidate.workloadRef.uid === uid);
  if (target) store.selectTarget(target.workloadRef);
}

function onSelectArtifact(artifactId: string): void {
  const artifact = store.artifacts.find((candidate) => candidate.id === artifactId);
  if (artifact) store.selectArtifact(artifact);
}

function onPolicyChange(policy: ConvergencePolicy): void {
  store.setConvergencePolicy(policy);
}

async function onConfirm(): Promise<void> {
  const result = await store.submit();
  if (result) {
    store.closeConfirm();
    await router.push(operationRoute(result.operationId));
  }
}
</script>

<template>
  <section class="emergency-page">
    <!--
      Direction C: the page had no h1 at all — only a hand-rolled breadcrumb nav with the
      title in a <strong>. PageHeader now owns the trail, the category (紧急) and the h1.
      The page carries no page-level explanatory copy of its own, so the subtitle is
      omitted rather than invented; the action slot stays empty because this surface has
      no header buttons (the only entries live in the flow).
    -->
    <PageHeader
      :title="t('emergency.page.title')"
      :eyebrow="t('emergency.page.eyebrow')"
      :crumbs="crumbs"
    />

    <LoadingState v-if="gate === 'loading'" message="正在加载授权与目标…" />
    <ForbiddenState
      v-else-if="gate === 'forbidden'"
      title="403"
      message="你没有执行紧急变更的权限（release.emergency.execute）。"
    />
    <ErrorState
      v-else-if="gate === 'not_found'"
      title="404"
      message="紧急变更不可用：功能已关闭或发布定义不存在。"
    />
    <div v-else-if="store.conflict?.hasConflict" class="conflict-block">
      <h2>{{ t('emergency.page.blockedTitle') }}</h2>
      <p>{{ t('emergency.page.blockedBody') }} {{ store.conflict.runningOperation?.type }}</p>
      <RouterLink
        v-if="store.conflict.runningOperation"
        :to="operationRoute(store.conflict.runningOperation.operationId)"
      >
        {{ t('emergency.page.viewOperation') }} {{ store.conflict.runningOperation.operationId }}
      </RouterLink>
    </div>
    <div v-else class="emergency-flow">
      <AuthorizationStaleNotice :stale="writeBlocked" />
      <h2>选择变更目标</h2>
      <EmergencyTargetSelector
        :targets="store.targets"
        :selected-uid="store.selectedTarget?.uid ?? null"
        :loading="store.loadingTargets"
        :error="store.loadError?.message ?? null"
        @select="onSelectTarget"
      />

      <template v-if="selectedTarget">
        <EmergencyActionSelector
          :target="selectedTarget"
          :selected="store.actionKind"
          @update:selected="store.setActionKind"
        />

        <template v-if="store.actionKind === 'image'">
          <!--
            The picker renders only when the read model carries container data.
            ListEmergencyTargets projects Containers from the operator's
            observation on the release_inventory row, so an empty list means no
            FRESH observation (never reported, a report without containers, or an
            observation older than the 15-minute staleness window) — not a missing
            column. Rendering the picker anyway produced a dead dropdown that
            looked usable while the target card beside it already reported the
            image capability as unavailable. The public release-inventory RPCs
            expose no workload field values at all; that restriction lives in
            ReleaseInventoryRow, not in this page's target read.
          -->
          <template v-if="selectedTarget.containers.length > 0">
            <h2>选择容器与制品</h2>
            <EmergencyArtifactSelector
              :containers="selectedTarget.containers"
              :selected-container="store.selectedContainer"
              :current-image-ref="store.selectedImageAction?.currentImageRef ?? ''"
              :artifacts="store.artifacts"
              :selected-artifact-id="store.selectedArtifact?.id ?? null"
              :loading="store.loadingArtifacts"
              :error="store.loadError?.message ?? null"
              @select-container="store.selectContainer"
              @select-artifact="onSelectArtifact"
            />
          </template>
          <p v-else class="unavailable-notice" role="status">
            镜像变更暂不可用：平台尚未采集到该工作负载的容器信息，因此无法选择容器与制品。
          </p>
        </template>

        <EmergencyReplicasInput
          v-else-if="store.actionKind === 'replicas'"
          :value="store.replicasValue"
          :current-replicas="store.replicasCurrent"
          :max="store.replicasMax"
          :available="store.replicasAvailable"
          :unavailable-reason="replicasUnavailableReason"
          :error="store.replicasError"
          @update:value="store.setReplicas"
        />

        <EmergencyAnnotationEditor
          v-else
          :approved-keys="approvedAnnotationKeyNames"
          :scope="store.annotationScope"
          :values="store.annotationEntries"
          :available-scopes="store.annotationScopesAvailable"
          :observed-keys="observedAnnotationKeyNames"
          :whitelist-notice="annotationWhitelistNotice"
          :error="store.annotationError"
          @update="store.setAnnotationEntries"
          @update:scope="store.setAnnotationScope"
        />

        <h2>填写变更信息</h2>
        <EmergencyChangeForm
          :reason="store.reason"
          :convergence-policy="store.convergencePolicy"
          :require-promotion-available="store.requirePromotionAvailable"
          :mapping-complete="store.mappingComplete"
          :submit-error="submittingError"
          @update:reason="store.setReason"
          @update:policy="onPolicyChange"
        />

        <div class="submit-row">
          <button
            type="button"
            class="primary"
            :disabled="!store.canConfirm || writeBlocked"
            @click="store.openConfirm()"
          >
            确认变更
          </button>
        </div>
      </template>
    </div>

    <EmergencyConfirmDialog
      :open="store.confirmOpen"
      :workload="store.selectedTarget"
      :container="store.selectedContainer"
      :artifact="store.selectedArtifact"
      :reason="store.reason"
      :policy="store.effectivePolicy"
      :action-summary="actionSummary"
      :risk-accepted="store.riskAccepted"
      :submitting="store.submitting"
      :error="store.submitError"
      @cancel="store.closeConfirm()"
      @confirm="onConfirm"
      @update:risk-accepted="store.setRiskAccepted"
    />
  </section>
</template>

<style scoped>
/* Direction-C page root: the shell's `main` already pads 24px (12px at 375), so a page-local
   `padding: 1.25rem` was a second inset that doubled it. `min-width: 0` keeps the wide
   annotation table from pushing the document at 375. */
.emergency-page { display: grid; gap: 1.25rem; min-width: 0; }
/* PageHeader keeps a 20px bottom margin for pages that are not grids; the grid gap is the
   only space between the header and the flow here. */
.emergency-page :deep(.page-header) { margin-bottom: 0; }
/* The conflict block is a warning callout, so it keeps the warning palette; only the radius
   moves onto the direction-C token. */
.conflict-block { display: grid; gap: 0.75rem; padding: 1rem; border: 1px solid var(--color-warning-border-strong); border-radius: var(--r); background: var(--color-warning-surface); }
.emergency-flow { display: grid; gap: 1.25rem; min-width: 0; }
.submit-row { display: flex; justify-content: flex-end; }
.unavailable-notice { margin: 0; padding: 0.75rem 1rem; border: 1px solid var(--color-border-strong); border-radius: var(--r); background: var(--sf); color: var(--color-muted-strong); }
.primary { padding: 0.6rem 1.25rem; border: 0; border-radius: 0.375rem; background: var(--color-danger); color: var(--color-on-accent); }
/* TASK-394: a disabled primary used to be painted with the danger fill, so "you may not
   press this yet" read as "this is destructive" (and the ink stayed --on-accent, which on
   that fill measured below AA). Disabled is its own state, not a semantic colour. */
.primary:disabled { box-shadow: inset 0 0 0 1px var(--color-disabled-border); background: var(--color-disabled-bg); color: var(--color-disabled-text); cursor: not-allowed; }
</style>
