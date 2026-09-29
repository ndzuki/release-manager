<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
import { useAuthStore } from '@/stores/auth';
import { onMounted, ref } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
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

const BLANK_MAPPING: PromotionMappingView = { workloadKind: '', workloadName: '', container: '', field: '', valuesPath: '' };

onMounted(() => {
  void store.load();
});

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
      <button type="button" :disabled="store.loading" @click="applyFilters">
        {{ store.loading ? '刷新中…' : '刷新' }}
      </button>
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

    <p v-if="store.notice" class="definitions__notice" role="status" data-testid="definitions-notice">{{ store.notice }}</p>

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
            <button
              v-if="auth.canWrite"
              type="button"
              :disabled="Boolean(definition.promotionMappingsViolation)"
              :data-testid="`definition-edit-${definition.id}`"
              @click="startEdit(definition)"
            >
              编辑
            </button>
          </td>
        </tr>
      </tbody>
    </table>

    <div v-if="editing" class="definitions__editor" role="dialog" aria-labelledby="definitions-editor-title">
      <h2 id="definitions-editor-title">编辑 {{ editing.name }}（版本 {{ editing.version }}）</h2>
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
              <td><input v-model="mapping.workloadKind" :name="`kind-${index}`" /></td>
              <td><input v-model="mapping.workloadName" :name="`name-${index}`" /></td>
              <td><input v-model="mapping.container" :name="`container-${index}`" /></td>
              <td><input v-model="mapping.field" :name="`field-${index}`" /></td>
              <td><input v-model="mapping.valuesPath" :name="`valuesPath-${index}`" /></td>
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

.definitions__editor {
  display: grid;
  gap: var(--space-3);
  padding: var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-surface);
}

.definitions__editor h2 {
  margin: 0;
  font-size: var(--font-size-lg);
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
