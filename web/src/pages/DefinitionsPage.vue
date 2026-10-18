<script setup lang="ts">
import { t } from '@/i18n/messages';
import { useAuthStore } from '@/stores/auth';
import { onMounted, ref } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import Toast from '@/components/common/Toast.vue';
import DefinitionCreateDialog from '@/components/definitions/DefinitionCreateDialog.vue';
import DefinitionDisableDialog from '@/components/definitions/DefinitionDisableDialog.vue';
import DefinitionEditorDialog from '@/components/definitions/DefinitionEditorDialog.vue';
import DefinitionTable from '@/components/definitions/DefinitionTable.vue';
import { useDefinitionCreate } from '@/composables/useDefinitionCreate';
import { useDefinitionDisable } from '@/composables/useDefinitionDisable';
import { useDefinitionEditor } from '@/composables/useDefinitionEditor';
import { useDefinitionsStore } from '@/stores/definitions';

/*
 * Release definitions and their promotion mappings (REQ-040, A9 of the UX plan).
 *
 * The promotion mapping is what lets a converged emergency change be promoted into a
 * standard ValuesRevision, so this page is the configuration prerequisite the plan
 * called out. The mapping is stored as JSON bytes on the read message; a payload we
 * cannot decode is a CONTRACT VIOLATION (per the proto comment), so those rows are
 * flagged and their editor is disabled rather than silently emptied.
 *
 * TASK-363: the page is a composition surface. The list, the three dialogs and the
 * editor/create/disable state live in components/definitions and composables; what
 * stays here is the store wiring, the filters and the page-level states.
 */
const store = useDefinitionsStore();
const auth = useAuthStore();

const customerId = ref('');
const clusterId = ref('');
const includeDisabled = ref(true);

const {
  editing,
  title: editorTitle,
  validationError,
  draftMappings,
  draftNamespace,
  draftReleaseName,
  draftChartName,
  startEdit,
  close: closeEditor,
  addMapping,
  removeMapping,
  submit: submitEditor,
} = useDefinitionEditor({
  update: (input) => store.update(input),
  freshDefinition: (definitionId) => store.definitions.find((definition) => definition.id === definitionId),
});

const {
  creating,
  form: createForm,
  validationError: createValidation,
  failure: createFailure,
  open: startCreate,
  close: closeCreate,
  submit: submitCreate,
} = useDefinitionCreate({
  create: (input) => store.create(input),
  failureMessage: () => store.failure?.message ?? null,
});

const {
  pending: pendingDisable,
  failure: disableFailure,
  description: disableDescription,
  arm: armDisable,
  close: closeDisable,
  confirm: confirmDisable,
} = useDefinitionDisable({
  disable: (definition) => store.disable(definition),
  freshDefinition: (definitionId) => store.definitions.find((definition) => definition.id === definitionId),
  failureMessage: () => store.failure?.message ?? null,
});

onMounted(() => {
  void store.load();
});

function applyFilters(): void {
  void store.load({ customerId: customerId.value.trim(), clusterId: clusterId.value.trim(), includeDisabled: includeDisabled.value });
}
</script>

<template>
  <section class="definitions">
    <header class="definitions__header">
      <div>
        <p class="eyebrow">{{ t('definitions.eyebrow') }}</p>
        <h1>发布定义</h1>
        <p class="definitions__subtitle">
          每个定义把 Bundle 来源与客户/集群/namespace/release 名称绑定；<strong>{{ t('definitions.promotionMapping') }}</strong>
          决定收敛后的紧急变更如何提升为标准 ValuesRevision。
        </p>
      </div>
      <div class="definitions__header-actions">
        <button v-if="auth.canWrite" type="button" data-testid="definitions-create-open" @click="startCreate">
          {{ t('definitions.create.open') }}
        </button>
        <button type="button" :disabled="store.loading" @click="applyFilters">
          {{ store.loading ? '刷新中…' : '刷新' }}
        </button>
      </div>
    </header>

    <form class="definitions__filters" @submit.prevent="applyFilters">
      <label>
        客户 ID（可空）
        <input v-model="customerId" name="customerId" :placeholder="t('definitions.customerId')" />
      </label>
      <label>
        集群 ID（可空）
        <input v-model="clusterId" name="clusterId" :placeholder="t('definitions.clusterId')" />
      </label>
      <label class="definitions__checkbox">
        <input v-model="includeDisabled" name="includeDisabled" type="checkbox" />
        包含已停用
      </label>
      <button type="submit" :disabled="store.loading">查询</button>
    </form>

    <!--
      TASK-299: the primitive, used without `auto-dismiss` — the store's notice is
      persistent today and stays persistent here. `tone="success"` keeps the assertive/
      polite mapping right for a successful update; the page's own class still owns the
      colours, so the box is unchanged.
    -->
    <Toast v-if="store.notice" class="definitions__notice" tone="success" data-testid="definitions-notice">
      {{ store.notice }}
    </Toast>

    <ErrorState
      v-if="store.failure"
      title="操作未成功"
      :message="store.failure.message"
      :details="store.failure.details"
      action-label="刷新列表"
      @action="applyFilters"
    />

    <p v-if="store.violations.length > 0" class="definitions__violation" role="alert" data-testid="definitions-violation">
      有 {{ store.violations.length }} 个定义的 Promotion Mapping 无法解析（契约违反）：{{
        store.violations.map((definition) => definition.name).join('、')
      }}。这些行已禁用编辑，避免把损坏数据写回。
    </p>

    <LoadingState v-if="store.loading && store.definitions.length === 0" message="正在读取发布定义…" />
    <EmptyState
      v-else-if="store.isEmpty"
      title="没有发布定义"
      message="当前过滤条件下没有定义；清空客户/集群过滤可查看组织内全部定义。"
    />

    <DefinitionTable
      v-else-if="store.definitions.length > 0"
      :definitions="store.definitions"
      :can-write="auth.canWrite"
      :saving="store.saving"
      @edit="startEdit"
      @disable="armDisable"
    />

    <DefinitionEditorDialog
      v-model:namespace="draftNamespace"
      v-model:release-name="draftReleaseName"
      v-model:chart-name="draftChartName"
      v-model:mappings="draftMappings"
      :open="editing !== null"
      :title="editorTitle"
      :saving="store.saving"
      :validation-error="validationError"
      @close="closeEditor"
      @submit="submitEditor"
      @add-mapping="addMapping"
      @remove-mapping="removeMapping"
    />

    <DefinitionCreateDialog
      v-model:form="createForm"
      :open="creating"
      :saving="store.saving"
      :validation-error="createValidation"
      :failure="createFailure"
      @close="closeCreate"
      @submit="submitCreate"
    />

    <DefinitionDisableDialog
      v-if="pendingDisable"
      :description="disableDescription"
      :failure="disableFailure"
      :saving="store.saving"
      @close="closeDisable"
      @confirm="confirmDisable"
    />
  </section>
</template>

<style scoped>
.definitions {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-5);
}

.definitions__header {
  display: flex;
  gap: var(--space-3);
  align-items: flex-start;
  justify-content: space-between;
}

.definitions__header h1 {
  margin: 0;
  font-size: var(--font-size-xl);
}

.eyebrow {
  margin: 0;
  color: var(--color-primary);
  font-size: var(--font-size-xs);
  font-weight: 800;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.definitions__subtitle {
  margin: var(--space-1) 0 0;
  max-width: 52rem;
  color: var(--color-muted);
}

.definitions__filters {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  align-items: flex-end;
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

.definitions__filters label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.definitions__header-actions {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.definitions__checkbox {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.definitions__filters input {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.definitions__notice {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-success-border);
  border-radius: var(--radius-lg);
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

.definitions__violation {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-warning-border);
  border-radius: var(--radius-lg);
  background: var(--color-warning-surface);
  color: var(--color-warning-ink-strong);
}

.definitions__filters button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}
</style>
