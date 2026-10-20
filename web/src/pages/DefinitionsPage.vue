<script setup lang="ts">
import { t } from '@/i18n/messages';
import { useAuthStore } from '@/stores/auth';
import { computed, onMounted, ref } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import PageHeader from '@/components/common/PageHeader.vue';
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

/*
 * Direction C (TASK-394). The hand-rolled header (eyebrow + h1 + a subtitle carrying a
 * <strong>) becomes PageHeader. PageHeader takes the subtitle as a string, so the old
 * emphasis on "Promotion Mapping" is gone; the wording is carried over verbatim. The
 * eyebrow and the h1 were already the same word ("发布定义") before the migration, so the
 * duplication below is preserved rather than introduced — changing it is a copy decision,
 * not part of the shell migration.
 *
 * The page is the top of its route, so it has no breadcrumb; a crumb built from route
 * params would print an id, which is the anti-pattern PageHeader exists to stop.
 */
const pageSubtitle = computed(
  () =>
    '每个定义把 Bundle 来源与客户、集群、命名空间与 Release 名称绑定；' +
    t('definitions.promotionMapping') +
    ' 决定收敛后的紧急变更如何提升为标准 Values 版本。',
);

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
    <PageHeader title="发布定义" :eyebrow="t('definitions.eyebrow')" :subtitle="pageSubtitle">
      <template #actions>
        <div class="definitions__header-actions">
          <button v-if="auth.canWrite" type="button" data-testid="definitions-create-open" @click="startCreate">
            {{ t('definitions.create.open') }}
          </button>
          <button type="button" :disabled="store.loading" @click="applyFilters">
            {{ store.loading ? '刷新中…' : '刷新' }}
          </button>
        </div>
      </template>
    </PageHeader>

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
      有 {{ store.violations.length }} 个定义的提升映射无法解析（数据异常）：{{
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
  min-width: 0;
}

/* PageHeader keeps a 20px bottom margin for pages that do not stack their sections in a
   grid; this page does, so the gap is the only space between the header and the filters.
   The shell's <main> already owns the page inset, so this page adds no padding of its own. */
.definitions :deep(.page-header) {
  margin-bottom: 0;
}

/* Direction C filter card: surface fill + the STRONG border (`--color-border` is a hairline
   and vanishes in the dark theme). */
.definitions__filters {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  align-items: flex-end;
  padding: var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
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
  flex-wrap: wrap;
  gap: var(--space-2);
  align-items: center;
}

/* The extra `.definitions__filters` is load-bearing: `.definitions__filters label` (0,1,1)
   is more specific than this class (0,1,0) and made the checkbox a grid, which stacked the
   box above its label. */
.definitions__filters .definitions__checkbox {
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
