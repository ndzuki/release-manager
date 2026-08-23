<script setup lang="ts">
// Convergence tasks page (plan v3 Step 7, AC-058-34~37/42): thin composition
// surface over the convergenceSelection store. The client pre-check is UX
// only; CreatePrepareSession stays the authoritative consistency boundary.
// On success the URL carries ONLY mode=convergence&prepareToken (AC-058-35).
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import ConvergenceTaskList from '@/components/emergency/ConvergenceTaskList.vue';
import { listValuesRevisions } from '@/connect/values-revision';
import { useAuthStore } from '@/stores/auth';
import { useConvergenceSelectionStore } from '@/stores/convergenceSelection';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';

const route = useRoute();
const router = useRouter();
const authStore = useAuthStore();
const authorization = useEmergencyAuthorizationStore();
const selection = useConvergenceSelectionStore();

const releaseDefinitionId = computed(() => String(route.params.releaseId ?? ''));
const customerId = computed(() => String(route.params.customerId ?? ''));
const organizationId = computed(() => authStore.activeOrganization?.id ?? '');

const gate = computed(() => authorization.gateFor('createValues'));
const parentVersion = ref(0n);
const preparing = computed(() => selection.preparing);

const routeScope = computed(() =>
  [route.params.customerId, route.params.clusterId, route.params.releaseId].map(String).join('/'),
);

watch(routeScope, async (current, previous) => {
  if (previous && current !== previous) {
    selection.reset();
  }
  if (!organizationId.value || !customerId.value || !releaseDefinitionId.value) return;
  await authorization.load(organizationId.value, customerId.value);
  if (authorization.gateFor('createValues') !== 'allowed') return;
  await selection.load(releaseDefinitionId.value);
  await loadParentVersion();
}, { immediate: true });

onBeforeUnmount(() => {
  selection.reset();
  authorization.reset();
});

/** Latest revision version is the CAS parent anchor for Prepare (AC-058-34). */
async function loadParentVersion(): Promise<void> {
  try {
    const revisions = await listValuesRevisions(releaseDefinitionId.value);
    const latest = revisions.reduce((max, revision) => Math.max(max, revision.revision), 0);
    parentVersion.value = BigInt(latest);
  } catch {
    parentVersion.value = 0n;
  }
}

async function prepare(): Promise<void> {
  const session = await selection.prepare(parentVersion.value);
  if (!session) return;
  await router.push({
    name: 'ValuesEditor',
    params: {
      customerId: customerId.value,
      clusterId: String(route.params.clusterId ?? ''),
      releaseId: releaseDefinitionId.value,
    },
    query: { mode: 'convergence', prepareToken: session.prepareToken },
  });
}

function continueToRevision(): void {
  void router.push({
    name: 'ValuesEditor',
    params: {
      customerId: customerId.value,
      clusterId: String(route.params.clusterId ?? ''),
      releaseId: releaseDefinitionId.value,
    },
  });
}
</script>

<template>
  <section class="convergence-page">
    <nav class="breadcrumbs" aria-label="Breadcrumb">
      <RouterLink
        :to="{ name: 'ReleaseInventory', params: { customerId, clusterId: route.params.clusterId } }"
      >
        Releases
      </RouterLink>
      <span aria-hidden="true">/</span><strong>Convergence Tasks</strong>
    </nav>

    <LoadingState v-if="gate === 'loading'" message="正在加载收敛任务…" />
    <ForbiddenState
      v-else-if="gate === 'forbidden'"
      title="403"
      message="你没有创建 ValuesRevision 收敛的权限（canCreateValuesRevision）。"
    />
    <div v-else class="convergence-body">
      <h1>待收敛任务（{{ releaseDefinitionId }}）</h1>
      <p v-if="selection.loadError" class="error-text" role="alert">{{ selection.loadError.message }}</p>

      <ConvergenceTaskList
        :tasks="selection.tasks"
        :selected-task-ids="selection.selectedTaskIds"
        @toggle="selection.toggleTask"
        @continue="continueToRevision"
      />

      <p v-if="!selection.compatibility.valid" class="error-text" role="alert">
        选择不兼容：{{ selection.compatibility.conflicts.map((conflict) => conflict.reason).join('；') }}
      </p>
      <p v-if="selection.prepareError" class="error-text" role="alert">
        {{ selection.prepareError.message }}
      </p>

      <div class="prepare-row">
        <span>已选择 {{ selection.selectedTaskIds.length }} 个任务</span>
        <button
          type="button"
          class="primary"
          :disabled="!selection.canPrepare"
          @click="prepare"
        >
          {{ preparing ? '准备中…' : 'Prepare 收敛' }}
        </button>
      </div>
    </div>
  </section>
</template>

<style scoped>
.convergence-page { display: grid; gap: 1.25rem; padding: 1.25rem; }
.breadcrumbs { display: flex; gap: 0.5rem; color: #64748b; }
.convergence-body { display: grid; gap: 1rem; }
.prepare-row { display: flex; justify-content: space-between; align-items: center; }
.primary { padding: 0.6rem 1.25rem; border: 0; border-radius: 0.375rem; background: #7c3aed; color: #fff; }
.primary:disabled { background: #c4b5fd; cursor: not-allowed; }
.error-text { color: #b91c1c; }
</style>
