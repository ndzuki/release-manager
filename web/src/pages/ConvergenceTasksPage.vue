<script setup lang="ts">
import { t } from '@/i18n/messages';
// Convergence tasks page (plan v3 Step 7, AC-058-34~37/42): thin composition
// surface over the convergenceSelection store. The client pre-check is UX
// only; CreatePrepareSession stays the authoritative consistency boundary.
// On success the URL carries ONLY mode=convergence&prepareToken (AC-058-35).
import { computed, onBeforeUnmount, ref, watch } from 'vue';
import { useRoute, useRouter, type RouteLocationRaw } from 'vue-router';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import AuthorizationStaleNotice from '@/components/common/AuthorizationStaleNotice.vue';
import CopyableHash from '@/components/common/CopyableHash.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import ConvergenceTaskList from '@/components/emergency/ConvergenceTaskList.vue';
import { listValuesRevisions } from '@/connect/values-revision';
import { useAuthStore } from '@/stores/auth';
import { useConvergenceSelectionStore } from '@/stores/convergenceSelection';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';

const route = useRoute();
const router = useRouter();
const authStore = useAuthStore();
const authorization = useEmergencyAuthorizationStore();
// AC-033-10: convergence tasks stay readable, but the state is surfaced so a
// disabled write entry is explained rather than mysterious.
const writeBlocked = computed(() => !authorization.writeAllowed);
const selection = useConvergenceSelectionStore();

const releaseDefinitionId = computed(() => String(route.params.releaseId ?? ''));
const customerId = computed(() => String(route.params.customerId ?? ''));
const organizationId = computed(() => authStore.activeOrganization?.id ?? '');

const gate = computed(() => authorization.gateFor('createValues'));
const parentVersion = ref(0n);
const preparing = computed(() => selection.preparing);

/*
 * TASK-394 (consistency wave 5): the hand-rolled `<nav class="breadcrumbs">` became
 * PageHeader's `crumbs`. The trail labels and the release-inventory target are derived
 * INSIDE this computed, so they are re-evaluated on every param change instead of being
 * captured once at setup. Crumb text is a NAME only: PageHeader renders the last entry as
 * the current page and never prints a route identifier.
 */
const crumbs = computed<Array<{ label: string; to?: RouteLocationRaw }>>(() => [
  {
    label: t('cluster.detail.releases'),
    to: {
      name: 'ReleaseInventory',
      params: { customerId: customerId.value, clusterId: route.params.clusterId },
    },
  },
  { label: t('convergence.page.title') },
]);

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
    <PageHeader :title="t('convergence.page.pending')" :crumbs="crumbs">
      <template #actions>
        <!--
          TASK-394: the definition id left the h1 (a PageHeader title must not print a route
          identifier, and a raw UUID as the title was the anti-pattern the direction-C audit
          named). It is still the reader's only clue to WHICH definition they are converging,
          so it did not disappear: CopyableHash shows a truncated value with the full one in
          `title` and a copy control, next to the identity it belongs to.
        -->
        <div v-if="releaseDefinitionId" class="convergence-page__definition">
          <span class="convergence-page__definition-label">{{ t('convergence.page.definition') }}</span>
          <CopyableHash :value="releaseDefinitionId" />
        </div>
      </template>
    </PageHeader>

    <AuthorizationStaleNotice :stale="writeBlocked" />

    <LoadingState v-if="gate === 'loading'" message="正在加载收敛任务…" />
    <ForbiddenState
      v-else-if="gate === 'forbidden'"
      title="403"
      message="你没有创建 ValuesRevision 收敛的权限（canCreateValuesRevision）。"
    />
    <div v-else class="convergence-body">
      <!-- UX-035 (W15): while selection.load is running the page used to render the
           task table with zero rows, which reads as "nothing to converge" instead of
           "still loading". The three states now mirror the shared primitives (W14). -->
      <LoadingState v-if="selection.loading" message="正在加载收敛任务…" />
      <ErrorState v-else-if="selection.loadError" :message="selection.loadError.message" />
      <EmptyState
        v-else-if="selection.tasks.length === 0"
        :title="t('state.noData')"
        :message="t('convergence.page.empty')"
      />

      <ConvergenceTaskList
        v-else
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
          {{ preparing ? '准备中…' : t('convergence.page.prepare') }}
        </button>
      </div>
    </div>
  </section>
</template>

<style scoped>
/* Direction-C page root: the shell's `main` already pads 24px (12px at 375), so the page
   only stacks its sections. The old `padding: 1.25rem` was a second padding. */
.convergence-page { display: grid; gap: 1.25rem; min-width: 0; }
/* PageHeader keeps a 20px bottom margin for pages that are not grids; the grid gap is the
   only space between the header and the notice here. */
.convergence-page :deep(.page-header) { margin-bottom: 0; }
.convergence-body { display: grid; gap: 1rem; min-width: 0; }
.convergence-page__definition { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
.convergence-page__definition-label { color: var(--mu); font-size: var(--font-size-xs); }
.prepare-row { display: flex; flex-wrap: wrap; justify-content: space-between; gap: var(--space-3); align-items: center; }
.primary { padding: 0.6rem 1.25rem; border: 0; border-radius: 0.375rem; background: var(--color-violet); color: var(--color-on-accent); }
/* TASK-394: same defect as EmergencyChangePage — the disabled primary borrowed this
   page's violet accent as if it were a disabled colour. */
.primary:disabled { box-shadow: inset 0 0 0 1px var(--color-disabled-border); background: var(--color-disabled-bg); color: var(--color-disabled-text); cursor: not-allowed; }
.error-text { color: var(--color-error-strong); }
</style>
