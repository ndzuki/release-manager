<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
import { useAuthStore } from '@/stores/auth';
import { onMounted, ref, useId, computed } from 'vue';
import AppDialog from '@/components/common/AppDialog.vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import Toast from '@/components/common/Toast.vue';
import type { DefinitionView, PromotionMappingView } from '@/connect/definition-api';
import { useDefinitionsStore } from '@/stores/definitions';

/*
 * Release definitions and their promotion mappings (REQ-040, A9 of the UX plan).
 *
 * The promotion mapping is what lets a converged emergency change be promoted into a
 * standard ValuesRevision, so this page is the configuration prerequisite the plan
 * called out. The mapping is stored as JSON bytes on the read message; a payload we
 * cannot decode is a CONTRACT VIOLATION (per the proto comment), so those rows are
 * flagged and their editor is disabled rather than silently emptied.
 */
const store = useDefinitionsStore();
const auth = useAuthStore();


const customerId = ref('');
const clusterId = ref('');
const includeDisabled = ref(true);

const editing = ref<DefinitionView | null>(null);
const draftMappings = ref<PromotionMappingView[]>([]);
const draftNamespace = ref('');
const draftReleaseName = ref('');
const draftChartName = ref('');
const validationError = ref('');

/*
 * Ids for the mapping-row controls. The prefix comes from useId() so the ids stay
 * unique even if a second editor is mounted, and stable across re-renders (a bare
 * array index would collide with a second instance and remounting would renumber
 * every id, breaking any label reference held across the update).
 */
const mappingIdPrefix = useId();

// TASK-281: the editor is an AppDialog now, whose heading comes from this prop and
// carries its own `aria-labelledby`; the title is the same text the hand-rolled
// `<h2 id="definitions-editor-title">` used to render (never a bare, untitled dialog).
const editorTitle = computed(() =>
  editing.value
    ? t('definitions.editorTitle', { name: editing.value.name, version: String(editing.value.version) })
    : '',
);

function mappingFieldId(index: number, field: string): string {
  return `${mappingIdPrefix}-mapping-${index}-${field}`;
}

const BLANK_MAPPING: PromotionMappingView = { workloadKind: '', workloadName: '', container: '', field: '', valuesPath: '' };

/*
 * TASK-301: the console could list and edit a definition but had no way to CREATE one,
 * and a definition could only be disabled outside the console, although
 * CreateReleaseDefinition / DisableReleaseDefinition exist (orchestrator/v1:1257/:1283).
 *
 * The create form is a plain `dialog`; the disable confirmation is a `danger`
 * alertdialog and the destructive RPC runs only from its confirm action.
 */
const creating = ref(false);
const createValidation = ref('');
/*
 * The failure is kept in its own slot and rendered INSIDE the dialog: the page-level
 * <ErrorState> sits behind the fixed backdrop, so reading the reason there meant
 * cancelling out of a confirmation the user had not yet completed (TASK-297/TASK-303).
 */
const createFailure = ref<string | null>(null);
const createForm = ref({
  customerId: '',
  clusterId: '',
  namespace: '',
  releaseName: '',
  chartName: '',
  enabled: true,
});
// The create form's ids come from useId() for the same reason the editor's do: a
// second mounted instance must not collide with this one's labels.
const createIdPrefix = useId();

function createFieldId(field: string): string {
  return `${createIdPrefix}-create-${field}`;
}

/** Set by the row button; the disable RPC waits for the dialog's confirmation. */
const pendingDisable = ref<DefinitionView | null>(null);
const disableFailure = ref<string | null>(null);
const disableDescription = computed(() =>
  pendingDisable.value ? t('definitions.disable.description', { name: pendingDisable.value.name }) : '',
);

onMounted(() => {
  void store.load();
});

function startCreate(): void {
  createValidation.value = '';
  createFailure.value = null;
  createForm.value = { customerId: '', clusterId: '', namespace: '', releaseName: '', chartName: '', enabled: true };
  creating.value = true;
}

async function submitCreate(): Promise<void> {
  createValidation.value = '';
  createFailure.value = null;
  const form = createForm.value;
  // The server refuses a missing customer/cluster with INVALID_ARGUMENT and derives
  // the definition's NAME from release_name, so an empty one would create a nameless
  // row: all three are required here instead of spending a request.
  if (!form.customerId.trim() || !form.clusterId.trim() || !form.releaseName.trim()) {
    createValidation.value = t('definitions.create.required');
    return;
  }
  const ok = await store.create({
    customerId: form.customerId.trim(),
    clusterId: form.clusterId.trim(),
    namespace: form.namespace.trim(),
    releaseName: form.releaseName.trim(),
    chartName: form.chartName.trim(),
    enabled: form.enabled,
  });
  if (ok) {
    creating.value = false;
    return;
  }
  createFailure.value = store.failure?.message ?? null;
}

function armDisable(definition: DefinitionView): void {
  disableFailure.value = null;
  pendingDisable.value = definition;
}

function closeDisable(): void {
  disableFailure.value = null;
  pendingDisable.value = null;
}

async function confirmDisable(): Promise<void> {
  const target = pendingDisable.value;
  if (!target) return;
  disableFailure.value = null;
  if (!(await store.disable(target))) {
    disableFailure.value = store.failure?.message ?? null;
    // A lost race reloaded the list: adopt the version the server now holds, otherwise
    // the hinted retry would carry the same stale version and fail forever.
    const fresh = store.definitions.find((definition) => definition.id === target.id);
    if (fresh && fresh.version !== target.version) pendingDisable.value = fresh;
    return;
  }
  pendingDisable.value = null;
}

function applyFilters(): void {
  void store.load({ customerId: customerId.value.trim(), clusterId: clusterId.value.trim(), includeDisabled: includeDisabled.value });
}

function startEdit(definition: DefinitionView): void {
  if (definition.promotionMappingsViolation) return;
  editing.value = definition;
  draftMappings.value = definition.promotionMappings.map((mapping) => ({ ...mapping }));
  draftNamespace.value = definition.namespace;
  draftReleaseName.value = definition.releaseName;
  draftChartName.value = definition.chartName;
  validationError.value = '';
}

function addMapping(): void {
  draftMappings.value.push({ ...BLANK_MAPPING });
}

function removeMapping(index: number): void {
  draftMappings.value.splice(index, 1);
}

function validate(): boolean {
  const target = editing.value;
  if (!target) return false;
  /*
   * TASK-214 removed the two refusals that used to live here. Clearing is expressible
   * again: the identifier strings are `optional string` (present-and-empty = clear) and
   * the mappings travel in a presence-carrying wrapper (an empty list = clear), so the
   * page can submit what the operator asked for instead of blocking it.
   */
  for (const [index, mapping] of draftMappings.value.entries()) {
    if (!mapping.workloadKind.trim() || !mapping.workloadName.trim() || !mapping.field.trim() || !mapping.valuesPath.trim()) {
      validationError.value = `第 ${index + 1} 行映射缺少必填项（workload kind/name、field、values path）`;
      return false;
    }
  }
  validationError.value = '';
  return true;
}

async function submit(): Promise<void> {
  const target = editing.value;
  if (!target || !validate()) return;
  const ok = await store.update({
    definitionId: target.id,
    // Always send the version we read: that is what makes the write versioned.
    expectedVersion: target.version,
    namespace: draftNamespace.value.trim(),
    releaseName: draftReleaseName.value.trim(),
    chartName: draftChartName.value.trim(),
    promotionMappings: draftMappings.value.map((mapping) => ({
      workloadKind: mapping.workloadKind.trim(),
      workloadName: mapping.workloadName.trim(),
      container: mapping.container.trim(),
      field: mapping.field.trim(),
      valuesPath: mapping.valuesPath.trim(),
    })),
  });
  if (ok) {
    editing.value = null;
    return;
  }
  // A conflict refreshed the list but left this editor on the stale object, so the
  // hinted retry would fail forever: adopt the version the server now holds.
  const fresh = store.definitions.find((definition) => definition.id === target.id);
  if (fresh && fresh.version !== target.version) editing.value = fresh;
}

function formatTimestamp(value: string | null): string {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : '—';
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

    <table v-else-if="store.definitions.length > 0" class="definitions__table">
      <thead>
        <tr>
          <th scope="col">名称</th>
          <th scope="col">{{ t('definitions.span') }}</th>
          <th scope="col">{{ t('definitions.namespaceRelease') }}</th>
          <th scope="col">{{ t('definitions.chartName') }}</th>
          <th scope="col">状态</th>
          <th scope="col">版本</th>
          <th scope="col">{{ t('definitions.promotionMapping') }}</th>
          <th scope="col">操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="definition in store.definitions" :key="definition.id" :data-testid="`definition-row-${definition.id}`">
          <td>
            <strong>{{ definition.name }}</strong>
            <br />
            <small>{{ formatTimestamp(definition.updatedAt) }}</small>
          </td>
          <td>
            <code>{{ definition.customerId }}</code>
            <br />
            <small>{{ definition.clusterId }}</small>
          </td>
          <td>
            {{ definition.namespace }} / {{ definition.releaseName }}
          </td>
          <td>{{ definition.chartName || '—' }}</td>
          <td>{{ statusLabel('definition', definition.status) }}</td>
          <td>{{ definition.version }}</td>
          <td>
            <span v-if="definition.promotionMappingsViolation" class="definitions__bad">无法解析</span>
            <span v-else>{{ definition.promotionMappings.length }} 条</span>
          </td>
          <td>
            <div class="definitions__row-actions">
              <button
                v-if="auth.canWrite"
                type="button"
                :disabled="Boolean(definition.promotionMappingsViolation)"
                :data-testid="`definition-edit-${definition.id}`"
                @click="startEdit(definition)"
              >
                编辑
              </button>
              <!-- Only an active definition can be disabled; a disabled one is terminal
                   (the server has no re-enable procedure). -->
              <button
                v-if="auth.canWrite && definition.status === 'active'"
                type="button"
                class="danger"
                :disabled="store.saving"
                :data-testid="`definition-disable-${definition.id}`"
                @click="armDisable(definition)"
              >
                {{ t('definitions.disable.open') }}
              </button>
            </div>
          </td>
        </tr>
      </tbody>
    </table>

    <AppDialog
      :open="editing !== null"
      :title="editorTitle"
      :close-on-backdrop="false"
      :close-on-escape="!store.saving"
      @close="editing = null"
    >
      <div v-if="editing" class="definitions__editor">
        <p class="definitions__hint">
          提交时会带上读到的版本号；若期间被他人修改，服务端会拒绝（乐观锁），页面会刷新后提示重试。
        </p>
        <form @submit.prevent="submit">
          <div class="definitions__basics">
            <label>
              {{ t('definitions.namespace') }}
              <input v-model="draftNamespace" name="namespace" />
            </label>
            <label>
              {{ t('definitions.releaseName') }}
              <input v-model="draftReleaseName" name="releaseName" />
            </label>
            <label>
              {{ t('definitions.chartName') }}
              <input v-model="draftChartName" name="chartName" />
            </label>
          </div>

          <h3>{{ t('definitions.promotionMapping') }}（{{ draftMappings.length }} 条）</h3>
          <table class="definitions__mappings">
            <thead>
              <tr>
                <th scope="col">{{ t('definitions.workloadKind') }}</th>
                <th scope="col">{{ t('definitions.workloadName') }}</th>
                <th scope="col">{{ t('definitions.container') }}</th>
                <th scope="col">{{ t('definitions.field') }}</th>
                <th scope="col">{{ t('definitions.valuesPath') }}</th>
                <th scope="col"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(mapping, index) in draftMappings" :key="index" :data-testid="`mapping-row-${index}`">
                <!--
                  Each cell carries a visually-hidden <label for> instead of an aria-label:
                  the column header above is plain text, so the input has no accessible
                  name of its own, and the row index is what tells the five inputs of one
                  row apart from the next. Ids are derived from useId(), so a second
                  editor mounted in this app cannot collide with them (useId counters are per app, so a
                  second Vue app in the same document could).
                -->
                <td>
                  <label class="visually-hidden" :for="mappingFieldId(index, 'kind')">
                    {{ t('common.labelledRowField', { field: t('definitions.workloadKind'), index: index + 1 }) }}
                  </label>
                  <input :id="mappingFieldId(index, 'kind')" v-model="mapping.workloadKind" :name="`kind-${index}`" />
                </td>
                <td>
                  <label class="visually-hidden" :for="mappingFieldId(index, 'name')">
                    {{ t('common.labelledRowField', { field: t('definitions.workloadName'), index: index + 1 }) }}
                  </label>
                  <input :id="mappingFieldId(index, 'name')" v-model="mapping.workloadName" :name="`name-${index}`" />
                </td>
                <td>
                  <label class="visually-hidden" :for="mappingFieldId(index, 'container')">
                    {{ t('common.labelledRowField', { field: t('definitions.container'), index: index + 1 }) }}
                  </label>
                  <input :id="mappingFieldId(index, 'container')" v-model="mapping.container" :name="`container-${index}`" />
                </td>
                <td>
                  <label class="visually-hidden" :for="mappingFieldId(index, 'field')">
                    {{ t('common.labelledRowField', { field: t('definitions.field'), index: index + 1 }) }}
                  </label>
                  <input :id="mappingFieldId(index, 'field')" v-model="mapping.field" :name="`field-${index}`" />
                </td>
                <td>
                  <label class="visually-hidden" :for="mappingFieldId(index, 'values-path')">
                    {{ t('common.labelledRowField', { field: t('definitions.valuesPath'), index: index + 1 }) }}
                  </label>
                  <input
                    :id="mappingFieldId(index, 'values-path')"
                    v-model="mapping.valuesPath"
                    :name="`valuesPath-${index}`"
                  />
                </td>
                <td>
                  <button type="button" :data-testid="`mapping-remove-${index}`" @click="removeMapping(index)">删除</button>
                </td>
              </tr>
            </tbody>
          </table>

          <button type="button" data-testid="mapping-add" @click="addMapping">新增映射</button>

          <p v-if="validationError" class="definitions__error" role="alert" data-testid="definitions-validation">
            {{ validationError }}
          </p>

          <div class="definitions__actions">
            <button type="button" :disabled="store.saving" @click="editing = null">取消</button>
            <button type="submit" :disabled="store.saving" data-testid="definitions-submit">
              {{ store.saving ? '提交中…' : '保存' }}
            </button>
          </div>
        </form>
      </div>
    </AppDialog>

    <!--
      TASK-301: create a definition. A plain `dialog` (creating is not destructive);
      `close-on-backdrop` stays off so an accidental click cannot lose the form, and a
      submission in flight also blocks Escape.
    -->
    <AppDialog
      :open="creating"
      :title="t('definitions.create.title')"
      :close-on-backdrop="false"
      :close-on-escape="!store.saving"
      @close="creating = false"
    >
      <div class="definitions__create">
        <p class="definitions__hint">{{ t('definitions.create.hint') }}</p>
        <form @submit.prevent="submitCreate">
          <div class="definitions__basics">
            <div class="definitions__field">
              <label :for="createFieldId('customer')">{{ t('definitions.create.customerId') }}</label>
              <input :id="createFieldId('customer')" v-model="createForm.customerId" name="create-customerId" />
            </div>
            <div class="definitions__field">
              <label :for="createFieldId('cluster')">{{ t('definitions.create.clusterId') }}</label>
              <input :id="createFieldId('cluster')" v-model="createForm.clusterId" name="create-clusterId" />
            </div>
            <div class="definitions__field">
              <label :for="createFieldId('namespace')">{{ t('definitions.create.namespace') }}</label>
              <input :id="createFieldId('namespace')" v-model="createForm.namespace" name="create-namespace" />
            </div>
            <div class="definitions__field">
              <label :for="createFieldId('release')">{{ t('definitions.create.releaseName') }}</label>
              <input :id="createFieldId('release')" v-model="createForm.releaseName" name="create-releaseName" />
            </div>
            <div class="definitions__field">
              <label :for="createFieldId('chart')">{{ t('definitions.create.chartName') }}</label>
              <input :id="createFieldId('chart')" v-model="createForm.chartName" name="create-chartName" />
            </div>
          </div>

          <label class="definitions__checkbox">
            <input v-model="createForm.enabled" name="create-enabled" type="checkbox" />
            {{ t('definitions.create.enabled') }}
          </label>

          <p v-if="createValidation" class="definitions__error" role="alert" data-testid="definitions-create-validation">
            {{ createValidation }}
          </p>
          <p v-if="createFailure" class="definitions__error" role="alert" data-testid="definitions-create-error">
            {{ createFailure }}
          </p>

          <div class="definitions__actions">
            <button type="button" :disabled="store.saving" @click="creating = false">
              {{ t('action.cancel') }}
            </button>
            <button type="submit" :disabled="store.saving" data-testid="definitions-create-submit">
              {{ store.saving ? t('definitions.create.submitting') : t('definitions.create.submit') }}
            </button>
          </div>
        </form>
      </div>
    </AppDialog>

    <!--
      TASK-301: disable a definition. Destructive and terminal, so the confirmation is
      a `danger` alertdialog; it is rendered only while armed, and the destructive RPC
      runs from `confirmDisable` — never from the row button.
    -->
    <AppDialog
      v-if="pendingDisable"
      :open="true"
      :title="t('definitions.disable.title')"
      :description="disableDescription"
      danger
      :close-on-backdrop="false"
      :close-on-escape="!store.saving"
      @close="closeDisable"
    >
      <p v-if="disableFailure" class="definitions__error" role="alert" data-testid="definitions-disable-error">
        {{ disableFailure }}
      </p>
      <template #footer>
        <button type="button" :disabled="store.saving" @click="closeDisable">
          {{ t('action.cancel') }}
        </button>
        <button
          type="button"
          class="definitions__confirm-danger"
          :disabled="store.saving"
          data-testid="definitions-disable-confirm"
          @click="confirmDisable"
        >
          {{ store.saving ? t('definitions.disable.submitting') : t('definitions.disable.confirm') }}
        </button>
      </template>
    </AppDialog>
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

.definitions__filters label,
.definitions__basics label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

/* TASK-301: the create form stacks each label over its input explicitly, so the
   label still has to be associated by `for` (the filter row keeps wrapping labels). */
.definitions__field {
  display: grid;
  gap: var(--space-1);
}

.definitions__field label {
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.definitions__header-actions,
.definitions__row-actions {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.definitions__row-actions button.danger {
  border-color: var(--color-danger-border);
  color: var(--color-error);
}

/* AppDialog's footer supplies a baseline for slotted buttons; the danger tone is the
   caller's rule (same split as BindingsPage's confirm). */
.definitions__confirm-danger {
  border-color: var(--color-danger-border);
  color: var(--color-error);
}

.definitions__create {
  display: grid;
  gap: var(--space-3);
}

.definitions__create form {
  display: grid;
  gap: var(--space-3);
}

.definitions__checkbox {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.definitions__filters input,
.definitions__basics input,
.definitions__mappings input {
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

.definitions__table,
.definitions__mappings {
  width: 100%;
  border-collapse: collapse;
  background: var(--color-surface);
}

.definitions__table th,
.definitions__table td,
.definitions__mappings th,
.definitions__mappings td {
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border);
  text-align: left;
}

.definitions__table small,
.definitions__hint {
  color: var(--color-muted);
}

.definitions__bad {
  color: var(--color-error);
  font-weight: var(--font-weight-bold);
}

.definitions__table button,
.definitions__filters button,
.definitions__actions button,
.definitions__mappings button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

/* AppDialog's panel already supplies the surface, padding and radius; keeping the
   old box here would render a box inside the box. It remains the layout/query hook. */
.definitions__editor {
  display: grid;
  gap: var(--space-3);
}

.definitions__editor h3 {
  margin: var(--space-2) 0 0;
  font-size: var(--font-size-md);
}

.definitions__editor form {
  display: grid;
  gap: var(--space-3);
}

.definitions__basics {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
}

.definitions__error {
  margin: 0;
  color: var(--color-error);
  font-size: var(--font-size-sm);
}

.definitions__actions {
  display: flex;
  gap: var(--space-3);
  justify-content: flex-end;
}
</style>
