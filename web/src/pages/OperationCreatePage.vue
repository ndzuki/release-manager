<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import type { RouteLocationRaw } from 'vue-router';
import Alert from '@/components/common/Alert.vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import Steps from '@/components/common/Steps.vue';
import OperationConfirmPanel from '@/components/operations/OperationConfirmPanel.vue';
import OperationForm from '@/components/operations/OperationForm.vue';
import { useScopeNames } from '@/composables/useScopeNames';
import { useOperationFormStore } from '@/stores/operationForm';

const route = useRoute();
const router = useRouter();
const store = useOperationFormStore();

/*
 * NAMES, never identifiers (TASK-394). The inventory link carries customerName,
 * clusterName and releaseName; a direct URL resolves the first two through the stores
 * and the release crumb is dropped when there is no name — the old fallback printed the
 * release-definition UUID as a breadcrumb level, which the direction-C audit called out.
 */
const { customerId, clusterId, customerName, clusterName } = useScopeNames();
const releaseDefinitionId = computed(() => String(route.params.releaseId ?? ''));
const releaseName = computed(() => (typeof route.query.releaseName === 'string' ? route.query.releaseName : ''));
const currentRevision = computed(() => {
  const revision = Number(route.query.currentRevision ?? 0);
  return Number.isInteger(revision) && revision > 0 ? revision : null;
});

const inventoryRoute = computed<RouteLocationRaw>(() => ({
  name: 'ReleaseInventory',
  params: { customerId: customerId.value, clusterId: clusterId.value },
  query: { customerName: customerName.value, clusterName: clusterName.value },
}));

const crumbs = computed<Array<{ label: string; to?: RouteLocationRaw }>>(() => {
  const trail: Array<{ label: string; to?: RouteLocationRaw }> = [
    { label: customerName.value },
    { label: clusterName.value },
  ];
  if (releaseName.value) trail.push({ label: releaseName.value, to: inventoryRoute.value });
  // The last crumb is the current page; PageHeader never links it.
  trail.push({ label: t('operation.create.eyebrow') });
  return trail;
});

type StepState = 'done' | 'now' | 'todo';

/*
 * Step states are DERIVED, not hard-coded. `validate()` is the same single source of
 * truth the submit path uses, so "done" means "the store would accept these fields
 * right now": a step whose fields pass is done, the first unfinished step is `now`
 * (Steps translates that into aria-current="step"), and everything after it is todo.
 * Step 1 is complete because a type is always selected (INSTALL is the default) and
 * changing it re-runs the same derivation.
 */
const formErrors = computed(() => store.validate());
const typeSelected = computed(() =>
  store.fields.operationType === 'INSTALL' ||
  store.fields.operationType === 'UPGRADE' ||
  store.fields.operationType === 'ROLLBACK',
);
// Step 2 is the type-specific required set: bundle + approved revision for INSTALL /
// UPGRADE, plus the current revision for UPGRADE, or the target revision for ROLLBACK.
const requiredReady = computed(() => {
  const errors = formErrors.value;
  return (
    errors.bundleId === undefined &&
    errors.valuesRevisionId === undefined &&
    errors.expectedCurrentRevision === undefined &&
    errors.targetRevision === undefined
  );
});
// A rollback renders no patch editor at all, so that step is vacuously complete for it.
const patchReady = computed(
  () => store.fields.operationType === 'ROLLBACK' || formErrors.value.patch === undefined,
);
const readyToConfirm = computed(() => requiredReady.value && patchReady.value);

const steps = computed<Array<{ label: string; state: StepState }>>(() => {
  // Completion is cumulative: a step is `done` only when it and everything before it
  // passes, so an optional step that is already valid cannot render "done" before the
  // required step it depends on. The first unfinished step is `now`; the rest are todo.
  const completions = [typeSelected.value, requiredReady.value, patchReady.value];
  const states: StepState[] = [];
  let previousDone = true;
  for (const complete of completions) {
    if (!previousDone) states.push('todo');
    else if (complete) states.push('done');
    else {
      states.push('now');
      previousDone = false;
    }
  }
  return [
    { label: t('operation.form.type'), state: states[0]! },
    { label: t('operation.create.step.artifact'), state: states[1]! },
    { label: t('operation.confirm.patch'), state: states[2]! },
    { label: t('operation.create.step.confirm'), state: readyToConfirm.value ? 'now' : 'todo' },
  ];
});

const confirmable = computed(() => store.step === 'confirm');

watch(releaseDefinitionId, (releaseId) => void store.setScope(releaseId), { immediate: true });

/*
 * Reaching the confirmation is a statement about the values on screen. Any edit after
 * that invalidates it, so the store goes back to the form step and the right-hand
 * confirmation returns to its disabled state instead of submitting a stale draft.
 */
watch(
  () => store.fields,
  () => {
    if (store.step === 'confirm') store.cancelConfirmation();
  },
  { deep: true },
);

function operationDetailRoute(operationId: string): RouteLocationRaw {
  return {
    name: 'OperationDetail',
    params: {
      customerId: customerId.value,
      clusterId: clusterId.value,
      releaseId: releaseDefinitionId.value,
      operationId,
    },
    query: {
      customerName: customerName.value,
      clusterName: clusterName.value,
      releaseName: releaseName.value,
    },
  };
}

async function confirmOperation(): Promise<void> {
  const operationId = await store.submit();
  if (!operationId) return;
  await router.push(operationDetailRoute(operationId));
}

async function viewExistingOperation(): Promise<void> {
  const operationId = store.submitError?.operationId;
  if (!operationId) return;
  await router.push(operationDetailRoute(operationId));
}

/*
 * While the form is being edited there is nothing to cancel here, so the action leaves
 * for the release inventory (the same destination as the header link). Once the
 * confirmation is open, cancel returns to the form with its values intact — the
 * behaviour the two-screen version had.
 */
async function cancel(): Promise<void> {
  if (store.step === 'confirm') {
    store.cancelConfirmation();
    return;
  }
  await router.push(inventoryRoute.value);
}
</script>

<template>
  <section class="operation-page">
    <PageHeader
      :title="t('operation.create.title')"
      :subtitle="t('operation.create.lead')"
      :crumbs="crumbs"
    >
      <template #actions>
        <RouterLink class="operation-page__back" :to="inventoryRoute">
          {{ t('operation.create.back') }}
        </RouterLink>
      </template>
    </PageHeader>

    <LoadingState v-if="store.optionsLoading" message="正在加载可用制品与配置…" />
    <ErrorState
      v-else-if="store.optionsError"
      title="操作选项加载失败"
      :message="store.optionsError"
      action-label="重试"
      @action="store.loadOptions"
    />
    <EmptyState
      v-else-if="store.isEmpty"
      title="没有可创建操作的选项"
      message="请先准备已验证的 Bundle；通过审批的 Values 版本会自动列在表单里。"
      action-label="重新加载"
      @action="store.loadOptions"
    />
    <template v-else>
      <Steps class="operation-page__steps" :steps="steps" />

      <div class="operation-page__grid">
        <section class="operation-page__form-region" aria-labelledby="operation-create-form-title">
          <h2 id="operation-create-form-title" class="operation-page__section-title">
            {{ t('operation.create.subtitle') }}
          </h2>

          <Alert
            v-if="store.submitError"
            tone="error"
            title="操作创建失败"
            :message="store.submitError.message"
          >
            <template #actions>
              <button
                v-if="store.submitError.operationId"
                type="button"
                class="operation-page__existing"
                @click="viewExistingOperation"
              >
                查看进行中操作
              </button>
              <button
                v-else-if="store.submitError.retryable"
                type="button"
                class="operation-page__retry"
                @click="confirmOperation"
              >
                重试
              </button>
            </template>
          </Alert>

          <OperationForm />
        </section>

        <aside class="operation-page__confirm">
          <OperationConfirmPanel
            :operation-type="store.fields.operationType"
            :customer-name="customerName"
            :cluster-name="clusterName"
            :release-name="releaseName"
            :bundle="store.selectedBundle"
            :values-revision-id="store.fields.valuesRevisionId ?? ''"
            :patch="store.fields.patch"
            :current-revision="store.fields.expectedCurrentRevision ?? currentRevision"
            :submitting="store.submitting"
            :confirmable="confirmable"
            @cancel="cancel"
            @confirm="confirmOperation"
          />
        </aside>
      </div>
    </template>
  </section>
</template>

<style scoped>
.operation-page {
  display: grid;
  gap: 18px;
}

.operation-page__back {
  display: inline-flex;
  align-items: center;
  padding: 7px 12px;
  border: 1px solid var(--bd);
  border-radius: 6px;
  background: var(--sf);
  color: var(--tx);
  font-weight: var(--font-weight-medium);
  text-decoration: none;
}

.operation-page__back:hover {
  border-color: var(--mu);
}

.operation-page__steps {
  padding: 12px 16px;
  border: 1px solid var(--bd);
  border-radius: var(--r);
  background: var(--sf);
}

/* Direction C two-column shape: the form gets the room, the confirmation is a sticky
   sidebar. `minmax(0, …)` on both tracks keeps a long release name from widening the
   grid; below 900px the layout collapses to one column and the sidebar un-sticks. */
.operation-page__grid {
  display: grid;
  grid-template-columns: minmax(0, 1.5fr) minmax(0, 1fr);
  gap: 18px;
  align-items: start;
}

.operation-page__form-region {
  display: grid;
  gap: 12px;
  min-width: 0;
}

.operation-page__section-title {
  margin: 0;
  color: var(--tx);
  font-size: var(--font-size-h2);
  font-weight: var(--font-weight-heading);
}

.operation-page__confirm {
  min-width: 0;
}

.operation-page__existing,
.operation-page__retry {
  padding: 6px 10px;
  border: 1px solid var(--bd);
  border-radius: 6px;
  background: var(--sf2);
  color: var(--tx);
  cursor: pointer;
}

@media (min-width: 901px) {
  .operation-page__confirm {
    position: sticky;
    top: 16px;
  }
}

@media (max-width: 900px) {
  .operation-page__grid {
    grid-template-columns: minmax(0, 1fr);
  }

  .operation-page__confirm {
    position: static;
  }
}
</style>
