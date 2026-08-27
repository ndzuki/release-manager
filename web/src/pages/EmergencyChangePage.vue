<script setup lang="ts">
// Emergency change page (plan v3 Step 4): thin composition surface —
// scoped authorization gate → conflict check → target/artifact/form flow →
// frozen confirmation → Execute → Operation Detail. All business logic lives
// in the stores; this page only wires route scope, gates and navigation.
import { computed, onBeforeUnmount, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import EmergencyArtifactSelector from '@/components/emergency/EmergencyArtifactSelector.vue';
import EmergencyChangeForm from '@/components/emergency/EmergencyChangeForm.vue';
import EmergencyConfirmDialog from '@/components/emergency/EmergencyConfirmDialog.vue';
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

const gate = computed(() => authorization.gateFor('execute'));
const selectedTarget = computed(() => store.selectedTargetDisplay);

const submittingError = computed(() => store.submitError);

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
    <nav class="breadcrumbs" aria-label="Breadcrumb">
      <RouterLink
        :to="{ name: 'ReleaseInventory', params: { customerId, clusterId: route.params.clusterId } }"
      >
        Releases
      </RouterLink>
      <span aria-hidden="true">/</span><strong>Emergency Change</strong>
    </nav>

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
      <h2>存在进行中的标准操作</h2>
      <p>
        发布定义存在 running
        {{ store.conflict.runningOperation?.type }} 操作，紧急变更入口已阻断（AC-058-08）。
      </p>
      <RouterLink
        v-if="store.conflict.runningOperation"
        :to="operationRoute(store.conflict.runningOperation.operationId)"
      >
        查看 Operation {{ store.conflict.runningOperation.operationId }}
      </RouterLink>
    </div>
    <div v-else class="emergency-flow">
      <h2>选择变更目标</h2>
      <EmergencyTargetSelector
        :targets="store.targets"
        :selected-uid="store.selectedTarget?.uid ?? null"
        :loading="store.loadingTargets"
        :error="store.loadError?.message ?? null"
        @select="onSelectTarget"
      />

      <template v-if="selectedTarget">
        <h2>选择容器与制品</h2>
        <EmergencyArtifactSelector
          :containers="selectedTarget.containers"
          :selected-container="store.selectedContainer"
          :artifacts="store.artifacts"
          :selected-artifact-id="store.selectedArtifact?.id ?? null"
          :loading="store.loadingArtifacts"
          :error="store.loadError?.message ?? null"
          @select-container="store.selectContainer"
          @select-artifact="onSelectArtifact"
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
            :disabled="!store.canConfirm"
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
.emergency-page { display: grid; gap: 1.25rem; padding: 1.25rem; }
.breadcrumbs { display: flex; gap: 0.5rem; color: #64748b; }
.conflict-block { display: grid; gap: 0.75rem; padding: 1rem; border: 1px solid #fde68a; border-radius: 0.5rem; background: #fffbeb; }
.emergency-flow { display: grid; gap: 1.25rem; }
.submit-row { display: flex; justify-content: flex-end; }
.primary { padding: 0.6rem 1.25rem; border: 0; border-radius: 0.375rem; background: #dc2626; color: #fff; }
.primary:disabled { background: #fca5a5; cursor: not-allowed; }
</style>
