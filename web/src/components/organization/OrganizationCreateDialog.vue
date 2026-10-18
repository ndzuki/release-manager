<script setup lang="ts">
import { useId } from 'vue';
import AppDialog from '@/components/common/AppDialog.vue';
import { t } from '@/i18n/messages';

/*
 * TASK-363: the create-organization dialog, carved out of OrganizationPage.
 *
 * Create is platform_admin-only and not idempotent; the page owns the reset-on-open
 * and the required-name check (composables/useOrganizationCreate). The failure
 * renders INSIDE this dialog because the page-level <ErrorState> sits behind the
 * fixed backdrop. `close-on-backdrop` stays off so an accidental click cannot lose
 * the form, and a submission in flight also blocks Escape.
 *
 * The field id comes from useId() for the same reason the editor's does on
 * DefinitionsPage: a second mounted instance must not collide with this one's label.
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

const name = defineModel<string>('name', { required: true });

const orgIdPrefix = useId();

function createOrgFieldId(field: string): string {
  return `${orgIdPrefix}-org-${field}`;
}
</script>

<template>
  <AppDialog
    :open="open"
    :title="t('organization.create.title')"
    :close-on-backdrop="false"
    :close-on-escape="!saving"
    @close="emit('close')"
  >
    <div class="organization__create">
      <p class="organization__hint">{{ t('organization.create.hint') }}</p>
      <form @submit.prevent="emit('submit')">
        <div class="organization__field">
          <label :for="createOrgFieldId('name')">{{ t('organization.create.name') }}</label>
          <input :id="createOrgFieldId('name')" v-model="name" name="organizationName" />
        </div>
        <p
          v-if="validationError"
          class="organization__error"
          role="alert"
          data-testid="organization-create-validation"
        >
          {{ validationError }}
        </p>
        <p v-if="failure" class="organization__error" role="alert" data-testid="organization-create-error">
          {{ failure }}
        </p>
        <div class="organization__actions">
          <button type="button" :disabled="saving" @click="emit('close')">
            {{ t('action.cancel') }}
          </button>
          <button type="submit" :disabled="saving" data-testid="organization-create-submit">
            {{ saving ? t('organization.create.submitting') : t('organization.create.submit') }}
          </button>
        </div>
      </form>
    </div>
  </AppDialog>
</template>

<style scoped>
.organization__create {
  display: grid;
  gap: var(--space-3);
}

.organization__create form {
  display: grid;
  gap: var(--space-3);
}

.organization__field {
  display: grid;
  gap: var(--space-1);
}

.organization__field label {
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.organization__create input {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.organization__create .organization__actions {
  justify-content: flex-end;
}

.organization__actions {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.organization__actions button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.organization__hint {
  margin: 0;
  color: var(--color-muted);
}

.organization__error {
  margin: 0;
  color: var(--color-error);
  font-size: var(--font-size-sm);
}
</style>
