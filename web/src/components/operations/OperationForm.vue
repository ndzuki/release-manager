<script setup lang="ts">
import { reactive } from 'vue';
import PatchOverrideEditor from './PatchOverrideEditor.vue';
import { useOperationFormStore, type OperationFormErrors } from '@/stores/operationForm';
import type { OperationType } from '@/types/operation';

const store = useOperationFormStore();
const errors = reactive<OperationFormErrors>({});
const operationTypes: OperationType[] = ['INSTALL', 'UPGRADE', 'ROLLBACK'];

function prepareConfirmation(): void {
  Object.keys(errors).forEach((key) => delete errors[key as keyof OperationFormErrors]);
  Object.assign(errors, store.openConfirmation());
}
</script>

<template>
  <form class="operation-form" @submit.prevent="prepareConfirmation">
    <fieldset class="operation-form__types">
      <legend>操作类型</legend>
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

    <label class="operation-form__field">
      制品 Bundle
      <select v-model="store.fields.bundleId" required>
        <option :value="null">请选择制品</option>
        <option v-for="bundle in store.availableBundles" :key="bundle.bundleId" :value="bundle.bundleId">
          {{ bundle.name }}@{{ bundle.chartVersion }} · {{ bundle.digest }}
        </option>
      </select>
      <span v-if="errors.bundleId" class="operation-form__error">{{ errors.bundleId }}</span>
    </label>

    <label class="operation-form__field">
      已审批 ValuesRevision ID
      <input
        v-model="store.fields.valuesRevisionId"
        type="text"
        aria-label="ValuesRevision ID"
        placeholder="vr-…"
        required
      />
      <span v-if="errors.valuesRevisionId" class="operation-form__error">{{ errors.valuesRevisionId }}</span>
    </label>

    <label v-if="store.fields.operationType !== 'INSTALL'" class="operation-form__field">
      当前 Revision
      <input v-model.number="store.fields.expectedCurrentRevision" type="number" min="1" required />
      <span v-if="errors.expectedCurrentRevision" class="operation-form__error">{{ errors.expectedCurrentRevision }}</span>
    </label>

    <PatchOverrideEditor
      v-if="store.fields.operationType !== 'ROLLBACK'"
      v-model="store.fields.patch"
      :error="errors.patch"
      :error-index="errors.patchIndex"
    />

    <button class="operation-form__submit" type="submit">检查并确认</button>
  </form>
</template>

<style scoped>
.operation-form { display: grid; gap: 1.25rem; padding: 1.5rem; border: 1px solid #e2e8f0; border-radius: 0.8rem; background: #fff; }
.operation-form__types { display: flex; flex-wrap: wrap; gap: 1rem; padding: 1rem; border: 1px solid #cbd5e1; border-radius: 0.65rem; }
.operation-form__types label { display: flex; align-items: center; gap: 0.4rem; font-weight: 700; }
.operation-form__field { display: grid; gap: 0.4rem; color: #334155; font-weight: 650; }
.operation-form__field select, .operation-form__field input { min-height: 2.6rem; padding: 0.55rem 0.7rem; border: 1px solid #94a3b8; border-radius: 0.4rem; background: #fff; }
.operation-form__error { color: #b91c1c; font-size: 0.85rem; font-weight: 500; }
.operation-form__submit { width: fit-content; justify-self: end; padding: 0.7rem 1rem; border: 0; border-radius: 0.45rem; background: #2563eb; color: #fff; font-weight: 700; }
</style>
