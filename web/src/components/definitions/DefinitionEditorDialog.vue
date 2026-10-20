<script setup lang="ts">
import { useId } from 'vue';
import { t } from '@/i18n/messages';
import AppDialog from '@/components/common/AppDialog.vue';
import type { PromotionMappingView } from '@/connect/definition-api';

/*
 * TASK-363: the edit dialog, carved out of DefinitionsPage.
 *
 * The page owns the draft state (composables/useDefinitionEditor) and passes it in
 * through the four `v-model` contracts; this component owns only the markup and the
 * per-instance control ids. The dialog cannot close itself: it emits `close` and the
 * page decides — a submission in flight must keep it open (TASK-282).
 */
defineProps<{
  open: boolean;
  title: string;
  saving: boolean;
  validationError: string;
}>();

const emit = defineEmits<{
  close: [];
  submit: [];
  'add-mapping': [];
  'remove-mapping': [index: number];
}>();

const draftNamespace = defineModel<string>('namespace', { required: true });
const draftReleaseName = defineModel<string>('releaseName', { required: true });
const draftChartName = defineModel<string>('chartName', { required: true });
const draftMappings = defineModel<PromotionMappingView[]>('mappings', { required: true });

/*
 * Ids for the mapping-row controls. The prefix comes from useId() so the ids stay
 * unique even if a second editor is mounted, and stable across re-renders (a bare
 * array index would collide with a second instance and remounting would renumber
 * every id, breaking any label reference held across the update).
 */
const mappingIdPrefix = useId();

function mappingFieldId(index: number, field: string): string {
  return `${mappingIdPrefix}-mapping-${index}-${field}`;
}
</script>

<template>
  <AppDialog
    :open="open"
    :title="title"
    :close-on-backdrop="false"
    :close-on-escape="!saving"
    @close="emit('close')"
  >
    <div v-if="open" class="definitions__editor">
      <p class="definitions__hint">
        提交时会带上读到的版本号；若期间被他人修改，服务端会拒绝（乐观锁），页面会刷新后提示重试。
      </p>
      <form @submit.prevent="emit('submit')">
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
        <div
          class="table-wrap"
          role="region"
          :aria-label="t('definitions.mappings.scrollRegion')"
          tabindex="0"
        >
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
                <button type="button" :data-testid="`mapping-remove-${index}`" @click="emit('remove-mapping', index)">删除</button>
              </td>
            </tr>
          </tbody>
          </table>
        </div>

        <button type="button" data-testid="mapping-add" @click="emit('add-mapping')">新增映射</button>

        <p v-if="validationError" class="definitions__error" role="alert" data-testid="definitions-validation">
          {{ validationError }}
        </p>

        <div class="definitions__actions">
          <button type="button" :disabled="saving" @click="emit('close')">取消</button>
          <button type="submit" :disabled="saving" data-testid="definitions-submit">
            {{ saving ? '提交中…' : '保存' }}
          </button>
        </div>
      </form>
    </div>
  </AppDialog>
</template>

<style scoped>
.definitions__basics {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
}

.definitions__basics label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.definitions__basics input,
.definitions__mappings input {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.definitions__mappings {
  width: 100%;
  border-collapse: collapse;
  background: var(--color-surface);
}

.definitions__mappings th,
.definitions__mappings td {
  padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid var(--color-border-strong);
  text-align: left;
}

.definitions__mappings button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.definitions__hint {
  color: var(--color-muted);
}

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

.definitions__error {
  margin: 0;
  color: var(--color-error-strong);
  font-size: var(--font-size-sm);
}

.definitions__actions {
  display: flex;
  gap: var(--space-3);
  justify-content: flex-end;
}

.definitions__actions button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}
</style>
