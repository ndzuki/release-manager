<script setup lang="ts">
import { useId } from 'vue';
import { t } from '@/i18n/messages';
import AppDialog from '@/components/common/AppDialog.vue';
import type { DefinitionCreateForm } from '@/composables/useDefinitionCreate';

/*
 * TASK-363: the create dialog, carved out of DefinitionsPage.
 *
 * A plain `dialog` (creating is not destructive); `close-on-backdrop` stays off so an
 * accidental click cannot lose the form, and a submission in flight also blocks
 * Escape. The page owns the form through `v-model:form` and the two failure slots:
 * the reason must render INSIDE this dialog because the page-level <ErrorState> sits
 * behind the fixed backdrop (TASK-297/TASK-303).
 */
defineProps<{
  open: boolean;
  saving: boolean;
  validationError: string;
  failure: string | null;
}>();

const emit = defineEmits<{
  close: [];
  submit: [];
}>();

const form = defineModel<DefinitionCreateForm>('form', { required: true });

// The create form's ids come from useId() for the same reason the editor's do: a
// second mounted instance must not collide with this one's labels.
const createIdPrefix = useId();

function createFieldId(field: string): string {
  return `${createIdPrefix}-create-${field}`;
}
</script>

<template>
  <AppDialog
    :open="open"
    :title="t('definitions.create.title')"
    :close-on-backdrop="false"
    :close-on-escape="!saving"
    @close="emit('close')"
  >
    <div class="definitions__create">
      <p class="definitions__hint">{{ t('definitions.create.hint') }}</p>
      <form @submit.prevent="emit('submit')">
        <div class="definitions__basics">
          <div class="definitions__field">
            <label :for="createFieldId('customer')">{{ t('definitions.create.customerId') }}</label>
            <input :id="createFieldId('customer')" v-model="form.customerId" name="create-customerId" />
          </div>
          <div class="definitions__field">
            <label :for="createFieldId('cluster')">{{ t('definitions.create.clusterId') }}</label>
            <input :id="createFieldId('cluster')" v-model="form.clusterId" name="create-clusterId" />
          </div>
          <div class="definitions__field">
            <label :for="createFieldId('namespace')">{{ t('definitions.create.namespace') }}</label>
            <input :id="createFieldId('namespace')" v-model="form.namespace" name="create-namespace" />
          </div>
          <div class="definitions__field">
            <label :for="createFieldId('release')">{{ t('definitions.create.releaseName') }}</label>
            <input :id="createFieldId('release')" v-model="form.releaseName" name="create-releaseName" />
          </div>
          <div class="definitions__field">
            <label :for="createFieldId('chart')">{{ t('definitions.create.chartName') }}</label>
            <input :id="createFieldId('chart')" v-model="form.chartName" name="create-chartName" />
          </div>
        </div>

        <label class="definitions__checkbox">
          <input v-model="form.enabled" name="create-enabled" type="checkbox" />
          {{ t('definitions.create.enabled') }}
        </label>

        <p v-if="validationError" class="definitions__error" role="alert" data-testid="definitions-create-validation">
          {{ validationError }}
        </p>
        <p v-if="failure" class="definitions__error" role="alert" data-testid="definitions-create-error">
          {{ failure }}
        </p>

        <div class="definitions__actions">
          <button type="button" :disabled="saving" @click="emit('close')">
            {{ t('action.cancel') }}
          </button>
          <button type="submit" :disabled="saving" data-testid="definitions-create-submit">
            {{ saving ? t('definitions.create.submitting') : t('definitions.create.submit') }}
          </button>
        </div>
      </form>
    </div>
  </AppDialog>
</template>

<style scoped>
.definitions__create {
  display: grid;
  gap: var(--space-3);
}

.definitions__create form {
  display: grid;
  gap: var(--space-3);
}

.definitions__basics {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
}

.definitions__basics input {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
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

.definitions__checkbox {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.definitions__hint {
  color: var(--color-muted);
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
