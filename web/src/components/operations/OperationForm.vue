<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, reactive } from 'vue';
import FormField from '@/components/common/FormField.vue';
import PatchOverrideEditor from './PatchOverrideEditor.vue';
import { useOperationFormStore, type OperationFormErrors } from '@/stores/operationForm';
import type { OperationType } from '@/types/operation';

const store = useOperationFormStore();
const errors = reactive<OperationFormErrors>({});
const operationTypes: OperationType[] = ['INSTALL', 'UPGRADE', 'ROLLBACK'];

/*
 * REQ-056 D10: the approved ValuesRevision is chosen, never typed. The form used to
 * take a free-text UUID, so an operator could submit an id the server would refuse
 * (values_not_approved) without ever seeing which revisions were actually approved.
 *
 * The status line has three distinct causes and each needs its own words: still
 * loading, the load failed, or the Release has no approved revision yet. It is wired
 * into the control's aria-describedby by appending its id to FormField's own.
 */
const revisionStatusId = 'operation-values-revision-status';
const revisionStatus = computed(() => {
  if (store.revisionsLoading) return t('operation.form.revisionLoading');
  if (store.revisionsError) return `${t('operation.form.revisionLoadFailed')}（${store.revisionsError}）`;
  if (store.approvedRevisions.length === 0) return t('operation.form.revisionEmpty');
  return '';
});
const revisionsDescribedBy = computed(() => (revisionStatus.value ? revisionStatusId : undefined));
const revisionStatusRole = computed(() => (store.revisionsError ? 'alert' : 'status'));

function prepareConfirmation(): void {
  Object.keys(errors).forEach((key) => delete errors[key as keyof OperationFormErrors]);
  Object.assign(errors, store.openConfirmation());
}
</script>

<template>
  <form class="operation-form" @submit.prevent="prepareConfirmation">
    <fieldset class="operation-form__types">
      <legend>{{ t('operation.form.type') }}</legend>
      <label v-for="operationType in operationTypes" :key="operationType">
        <input
          type="radio"
          name="operationType"
          :value="operationType"
          :checked="store.fields.operationType === operationType"
          @change="store.setOperationType(operationType)"
        />
        {{ operationType }}
      </label>
    </fieldset>

    <label v-if="store.fields.operationType !== 'ROLLBACK'" class="operation-form__field">
      {{ t('operation.form.bundle') }}
      <select v-model="store.fields.bundleId" required data-testid="operation-bundle">
        <option :value="null">{{ t('operation.form.selectArtifact') }}</option>
        <option v-for="bundle in store.availableBundles" :key="bundle.bundleId" :value="bundle.bundleId">
          {{ bundle.name }}@{{ bundle.chartVersion }} · {{ bundle.digest }}
        </option>
      </select>
      <span v-if="errors.bundleId" class="operation-form__error">{{ errors.bundleId }}</span>
    </label>

    <FormField
      v-if="store.fields.operationType !== 'ROLLBACK'"
      :label="t('operation.form.approvedRevision')"
      :error="errors.valuesRevisionId"
      required
    >
      <template #default="{ id, describedBy, invalid, disabled, required: fieldRequired }">
        <select
          :id="id"
          v-model="store.fields.valuesRevisionId"
          :aria-describedby="[describedBy, revisionsDescribedBy].filter(Boolean).join(' ') || undefined"
          :aria-invalid="invalid"
          :disabled="disabled || store.revisionsLoading || store.approvedRevisions.length === 0"
          :required="fieldRequired"
          data-testid="operation-values-revision"
        >
          <option :value="null">{{ t('operation.form.selectRevision') }}</option>
          <option
            v-for="revision in store.approvedRevisions"
            :key="revision.id"
            :value="revision.id"
            :data-testid="`operation-revision-option-${revision.id}`"
          >
            v{{ revision.revision }} · {{ revision.id }}
          </option>
        </select>
        <p v-if="revisionStatus" :id="revisionStatusId" :role="revisionStatusRole" class="operation-form__status">
          {{ revisionStatus }}
        </p>
      </template>
    </FormField>

    <label v-if="store.fields.operationType !== 'INSTALL'" class="operation-form__field">
      {{ t('operation.form.currentRevision') }}
      <input v-model.number="store.fields.expectedCurrentRevision" type="number" min="1" required />
      <span v-if="errors.expectedCurrentRevision" class="operation-form__error">{{ errors.expectedCurrentRevision }}</span>
    </label>

    <label v-if="store.fields.operationType === 'ROLLBACK'" class="operation-form__field">
      {{ t('operation.form.targetRevision') }}
      <input v-model.number="store.fields.targetRevision" type="number" min="1" required />
      <span v-if="errors.targetRevision" class="operation-form__error">{{ errors.targetRevision }}</span>
    </label>

    <PatchOverrideEditor
      v-if="store.fields.operationType !== 'ROLLBACK'"
      v-model="store.fields.patch"
      :error="errors.patch"
      :error-index="errors.patchIndex"
    />

    <button class="operation-form__submit" type="submit">{{ t('operation.form.checkAndConfirm') }}</button>
  </form>
</template>

<style scoped>
.operation-form { display: grid; gap: 1.25rem; padding: 1.5rem; border: 1px solid var(--color-border); border-radius: 0.8rem; background: var(--color-surface); }
.operation-form__types { display: flex; flex-wrap: wrap; gap: 1rem; padding: 1rem; border: 1px solid var(--color-border-strong); border-radius: 0.65rem; }
.operation-form__types label { display: flex; align-items: center; gap: 0.4rem; font-weight: 700; }
.operation-form__field { display: grid; gap: 0.4rem; color: var(--color-text-secondary); font-weight: 650; }
.operation-form__field select, .operation-form__field input { min-height: 2.6rem; padding: 0.55rem 0.7rem; border: 1px solid var(--color-subtle); border-radius: 0.4rem; background: var(--color-surface); }
.operation-form__status { margin: 0; color: var(--color-muted); font-size: var(--font-size-sm); }
.operation-form__error { color: var(--color-error); font-size: var(--font-size-sm); font-weight: 500; }
.operation-form__submit { width: fit-content; justify-self: end; padding: 0.7rem 1rem; border: 0; border-radius: 0.45rem; background: var(--color-primary); color: var(--color-on-accent); font-weight: 700; }
</style>
