<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
import { computed, nextTick, reactive, ref, useId } from 'vue';
import FormField from '@/components/common/FormField.vue';
import PatchOverrideEditor from './PatchOverrideEditor.vue';
import { useOperationFormStore, type OperationFormErrors } from '@/stores/operationForm';
import type { OperationType } from '@/types/operation';

const store = useOperationFormStore();
const errors = reactive<OperationFormErrors>({});
const operationTypes: OperationType[] = ['INSTALL', 'UPGRADE', 'ROLLBACK'];
// The segmented group is named by its visible legend, so the label a sighted reader sees
// is the same one assistive tech announces (no second, invisible aria-label to drift).
const typeLabelId = useId();

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

/*
 * W12 (UX-024 / UX-053). Two gaps the review measured, both about the distance
 * between "the form knows something is wrong" and "the operator sees it":
 *
 * ① VALIDATE ON BLUR, not only on submit. `validate()` is the store's single source
 *    of truth, so the blur handler re-runs it and copies back only the field the
 *    operator just left — a field they have not touched never grows an error.
 * ② FOCUS THE FIRST ERROR after a refused submit. The errors are rendered by
 *    FormField/PatchOverrideEditor, so the form owns no refs to the controls; it
 *    queries the DOM for the first invalid control (or the fieldset-level summary a
 *    patch error without an index renders) rather than duplicating the error map.
 */
type ValidatedField = 'bundleId' | 'valuesRevisionId' | 'expectedCurrentRevision' | 'targetRevision' | 'patch';

const formRef = ref<HTMLFormElement | null>(null);

function applyValidation(field: ValidatedField): void {
  const next = store.validate();
  if (field === 'patch') {
    if (next.patch === undefined) {
      delete errors.patch;
      delete errors.patchIndex;
      return;
    }
    errors.patch = next.patch;
    errors.patchIndex = next.patchIndex;
    return;
  }
  const message = next[field];
  if (message === undefined) delete errors[field];
  else errors[field] = message;
}

async function focusFirstError(): Promise<void> {
  await nextTick();
  formRef.value?.querySelector<HTMLElement>('[aria-invalid="true"], [data-field-error]')?.focus();
}

async function prepareConfirmation(): Promise<void> {
  Object.keys(errors).forEach((key) => delete errors[key as keyof OperationFormErrors]);
  Object.assign(errors, store.openConfirmation());
  if (Object.keys(errors).length > 0) await focusFirstError();
}
</script>

<template>
  <form ref="formRef" class="operation-form" @submit.prevent="prepareConfirmation">
    <!--
      TASK-394: the operation type is a segmented button group instead of three radios
      drawn as browser dots. Each button is `type="button"` (only the trailing submit may
      submit), carries `aria-pressed` for the selected state, and the group is named by
      the visible legend through `aria-labelledby` — the same words a sighted reader sees.
      Native buttons keep the control in the tab order and operable with Enter/Space.
    -->
    <div class="operation-form__type">
      <p :id="typeLabelId" class="operation-form__legend">{{ t('operation.form.type') }}</p>
      <div class="seg" role="group" :aria-labelledby="typeLabelId">
        <button
          v-for="operationType in operationTypes"
          :key="operationType"
          type="button"
          class="seg__button"
          :class="{ 'seg__button--selected': store.fields.operationType === operationType }"
          :aria-pressed="store.fields.operationType === operationType"
          :data-testid="`operation-type-${operationType}`"
          @click="store.setOperationType(operationType)"
        >
          {{ statusLabel('operationType', operationType) }}
        </button>
      </div>
    </div>

    <!--
     TASK-275: the error used to live INSIDE this implicitly wrapping <label>, so it was
     part of the control's accessible NAME and an aria-describedby pointing at the same
     node would announce it twice. FormField (TASK-268) owns the clean shape instead:
     `label for` + slotted control + `aria-describedby` on the rendered message +
     `role="alert"` on the error + `aria-invalid` only while there is one.
    -->
    <FormField
      v-if="store.fields.operationType !== 'ROLLBACK'"
      :label="t('operation.form.bundle')"
      :error="errors.bundleId"
      required
    >
      <template #default="{ id, describedBy, invalid, disabled, required: fieldRequired }">
        <select
          :id="id"
          v-model="store.fields.bundleId"
          class="operation-form__control"
          :aria-describedby="describedBy"
          :aria-invalid="invalid"
          :disabled="disabled"
          :required="fieldRequired"
          data-testid="operation-bundle"
          @blur="applyValidation('bundleId')"
        >
          <option :value="null">{{ t('operation.form.selectArtifact') }}</option>
          <option v-for="bundle in store.availableBundles" :key="bundle.bundleId" :value="bundle.bundleId">
            {{ bundle.name }}@{{ bundle.chartVersion }} · {{ bundle.digest }}
          </option>
        </select>
      </template>
    </FormField>

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
          class="operation-form__control"
          :aria-describedby="[describedBy, revisionsDescribedBy].filter(Boolean).join(' ') || undefined"
          :aria-invalid="invalid"
          :disabled="disabled || store.revisionsLoading || store.approvedRevisions.length === 0"
          :required="fieldRequired"
          data-testid="operation-values-revision"
          @blur="applyValidation('valuesRevisionId')"
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

    <!-- TASK-275: same shape as the bundle field above — the error is FormField's child, not the label's. -->
    <FormField
      v-if="store.fields.operationType !== 'INSTALL'"
      :label="t('operation.form.currentRevision')"
      :error="errors.expectedCurrentRevision"
      required
    >
      <template #default="{ id, describedBy, invalid, disabled, required: fieldRequired }">
        <input
          :id="id"
          v-model.number="store.fields.expectedCurrentRevision"
          class="operation-form__control"
          type="number"
          min="1"
          :aria-describedby="describedBy"
          :aria-invalid="invalid"
          :disabled="disabled"
          :required="fieldRequired"
          @blur="applyValidation('expectedCurrentRevision')"
        />
      </template>
    </FormField>

    <FormField
      v-if="store.fields.operationType === 'ROLLBACK'"
      :label="t('operation.form.targetRevision')"
      :error="errors.targetRevision"
      required
    >
      <template #default="{ id, describedBy, invalid, disabled, required: fieldRequired }">
        <input
          :id="id"
          v-model.number="store.fields.targetRevision"
          class="operation-form__control"
          type="number"
          min="1"
          :aria-describedby="describedBy"
          :aria-invalid="invalid"
          :disabled="disabled"
          :required="fieldRequired"
          @blur="applyValidation('targetRevision')"
        />
      </template>
    </FormField>

    <PatchOverrideEditor
      v-if="store.fields.operationType !== 'ROLLBACK'"
      v-model="store.fields.patch"
      :error="errors.patch"
      :error-index="errors.patchIndex"
      @blur="applyValidation('patch')"
    />

    <button class="operation-form__submit" type="submit">{{ t('operation.form.checkAndConfirm') }}</button>
  </form>
</template>

<style scoped>
.operation-form {
  display: grid;
  gap: 16px;
  padding: 18px;
  border: 1px solid var(--bd);
  border-radius: var(--r);
  background: var(--sf);
}

.operation-form__type {
  display: grid;
  gap: 8px;
}

.operation-form__legend {
  margin: 0;
  color: var(--mu);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-medium);
}

/*
 * Direction C segmented control: one recessed track with the selected segment lifted
 * onto the page background and outlined by the hairline, so the state reads in both
 * themes without a second accent colour competing with the primary submit.
 */
.seg {
  display: flex;
  gap: 2px;
  min-width: 0;
  padding: 3px;
  border: 1px solid var(--bd);
  border-radius: var(--r);
  background: var(--sf2);
}

.seg__button {
  flex: 1 1 0;
  min-width: 0;
  padding: 6px 10px;
  border: 0;
  border-radius: calc(var(--r) - 3px);
  background: transparent;
  color: var(--mu);
  font-size: var(--font-size-md);
  font-weight: var(--font-weight-medium);
  white-space: nowrap;
  cursor: pointer;
}

.seg__button:hover {
  color: var(--tx);
}

.seg__button--selected {
  background: var(--bg);
  color: var(--tx);
  box-shadow: 0 0 0 1px var(--bd);
}

/*
 * TASK-275: the three controls now live in FormField's slot, which compiles in THIS
 * component's scope, so a plain class rule still reaches them; the field chrome
 * (label/error/spacing) belongs to FormField.
 */
.operation-form__control {
  min-height: 2.6rem;
  padding: 8px 10px;
  border: 1px solid var(--bd);
  border-radius: 6px;
  background: var(--bg);
  color: var(--tx);
}

.operation-form__control::placeholder {
  color: var(--mu);
}

.operation-form__status {
  margin: 0;
  color: var(--mu);
  font-size: var(--font-size-sm);
}

.operation-form__submit {
  width: fit-content;
  justify-self: end;
  padding: 8px 14px;
  border: 1px solid var(--pr);
  border-radius: 6px;
  background: var(--pr);
  color: var(--on);
  font-weight: var(--font-weight-bold);
  cursor: pointer;
}
</style>
