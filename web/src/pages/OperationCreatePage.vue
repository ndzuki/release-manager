<script setup lang="ts">
import { computed, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import OperationConfirmPanel from '@/components/operations/OperationConfirmPanel.vue';
import OperationForm from '@/components/operations/OperationForm.vue';
import { useOperationFormStore } from '@/stores/operationForm';

const route = useRoute();
const router = useRouter();
const store = useOperationFormStore();

const customerId = computed(() => String(route.params.customerId ?? ''));
const clusterId = computed(() => String(route.params.clusterId ?? ''));
const releaseDefinitionId = computed(() => String(route.params.releaseId ?? ''));
const customerName = computed(() => String(route.query.customerName ?? customerId.value));
const clusterName = computed(() => String(route.query.clusterName ?? clusterId.value));
const releaseName = computed(() => String(route.query.releaseName ?? releaseDefinitionId.value));
const currentRevision = computed(() => {
  const revision = Number(route.query.currentRevision ?? 0);
  return Number.isInteger(revision) && revision > 0 ? revision : null;
});

watch(releaseDefinitionId, (releaseId) => void store.setScope(releaseId), { immediate: true });

async function confirmOperation(): Promise<void> {
  const operationId = await store.submit();
  if (!operationId) return;
  await router.push({
    name: 'OperationDetail',
    params: { customerId: customerId.value, clusterId: clusterId.value, releaseId: releaseDefinitionId.value, operationId },
    query: { customerName: customerName.value, clusterName: clusterName.value, releaseName: releaseName.value },
  });
}

async function viewExistingOperation(): Promise<void> {
  const operationId = store.submitError?.operationId;
  if (!operationId) return;
  await router.push({
    name: 'OperationDetail',
    params: { customerId: customerId.value, clusterId: clusterId.value, releaseId: releaseDefinitionId.value, operationId },
    query: { customerName: customerName.value, clusterName: clusterName.value, releaseName: releaseName.value },
  });
}
</script>

<template>
  <section class="operation-page">
    <nav class="operation-page__breadcrumbs" aria-label="Breadcrumb">
      <span>{{ customerName }}</span><span aria-hidden="true">/</span>
      <span>{{ clusterName }}</span><span aria-hidden="true">/</span>
      <span>{{ releaseName }}</span><span aria-hidden="true">/</span>
      <strong>New operation</strong>
    </nav>

    <header class="operation-page__header">
      <div>
        <p class="operation-page__eyebrow">Release operation</p>
        <h1>创建发布操作</h1>
        <p>选择制品和已审批配置，确认完整目标后启动 Preflight。</p>
      </div>
      <RouterLink :to="{ name: 'ReleaseInventory', params: { customerId, clusterId } }">返回 Releases</RouterLink>
    </header>

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
      message="请先准备 validated Bundle；已审批的 ValuesRevision ID 需从配置中心获取后手动填写。"
      action-label="重新加载"
      @action="store.loadOptions"
    />
    <template v-else>
      <OperationForm v-if="store.step === 'form'" />
      <OperationConfirmPanel
        v-else
        :operation-type="store.fields.operationType"
        :customer-name="customerName"
        :cluster-name="clusterName"
        :release-name="releaseName"
        :bundle="store.selectedBundle"
        :values-revision-id="store.fields.valuesRevisionId ?? ''"
        :patch="store.fields.patch"
        :current-revision="currentRevision ?? store.fields.expectedCurrentRevision"
        :submitting="store.submitting"
        @cancel="store.cancelConfirmation"
        @confirm="confirmOperation"
      />

      <ErrorState
        v-if="store.submitError"
        title="操作创建失败"
        :message="store.submitError.message"
      >
        <template #action>
          <button
            v-if="store.submitError.operationId"
            type="button"
            class="operation-page__existing"
            @click="viewExistingOperation"
          >
            查看进行中操作
          </button>
          <button v-else-if="store.submitError.retryable" type="button" @click="confirmOperation">重试</button>
        </template>
      </ErrorState>
    </template>
  </section>
</template>

<style scoped>
.operation-page { display: grid; gap: 1.5rem; max-width: 64rem; margin: 0 auto; }
.operation-page__breadcrumbs { display: flex; flex-wrap: wrap; gap: 0.45rem; color: #64748b; font-size: 0.85rem; }
.operation-page__header { display: flex; justify-content: space-between; align-items: flex-start; gap: 1rem; }
.operation-page__header h1, .operation-page__header p { margin: 0; }
.operation-page__header div { display: grid; gap: 0.35rem; }
.operation-page__eyebrow { color: #2563eb; font-size: 0.75rem; font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.operation-page__header a, .operation-page button { padding: 0.6rem 0.85rem; border: 1px solid #94a3b8; border-radius: 0.4rem; background: #fff; color: #0f172a; text-decoration: none; }
.operation-page__existing { border-color: #2563eb !important; color: #1d4ed8 !important; }
@media (max-width: 42rem) { .operation-page__header { flex-direction: column; } }
</style>
