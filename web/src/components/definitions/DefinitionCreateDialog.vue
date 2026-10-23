<script setup lang="ts">
import { useId } from 'vue';
import { t } from '@/i18n/messages';
import AppDialog from '@/components/common/AppDialog.vue';
import type { DefinitionCreateForm } from '@/composables/useDefinitionCreate';
import type { ClusterSummary } from '@/types/cluster';
import type { CustomerSummary } from '@/types/customer';
import { shortId } from '@/utils/shortId';

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
  /** Customers the picker offers; the page loads them once and passes them down. */
  customers: CustomerSummary[];
  /** Clusters of the currently picked customer (empty until one is picked). */
  clusters: ClusterSummary[];
  loadingCustomers: boolean;
  loadingClusters: boolean;
  customersError: string | null;
  clustersError: string | null;
}>();

const emit = defineEmits<{
  close: [];
  submit: [];
  /** The page owns the cluster list, so it reloads when the customer changes. */
  customerChange: [];
}>();

function onCustomerChange(): void {
  // A cluster belongs to exactly one customer: keeping the old value would submit a
  // pair the server rejects.
  form.value.clusterId = '';
  emit('customerChange');
}

function customerLabel(customer: CustomerSummary): string {
  return t('picker.customerLabel', { name: customer.name, slug: customer.slug || shortId(customer.id) });
}

function clusterLabel(cluster: ClusterSummary): string {
  return t('picker.clusterLabel', { name: cluster.name, id: shortId(cluster.id) });
}

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
            <label :for="createFieldId('customer')">{{ t('definitions.create.customer') }}</label>
            <select
              :id="createFieldId('customer')"
              v-model="form.customerId"
              name="create-customerId"
              :disabled="loadingCustomers"
              @change="onCustomerChange"
            >
              <option value="">{{ loadingCustomers ? t('picker.loading') : t('picker.customerPlaceholder') }}</option>
              <option v-for="customer in customers" :key="customer.id" :value="customer.id" :title="customer.id">
                {{ customerLabel(customer) }}
              </option>
            </select>
            <p v-if="customersError" class="definitions__error" role="alert">{{ customersError }}</p>
            <p v-else-if="form.customerId" class="definitions__picked">
              {{ t('picker.idLabel') }}<code>{{ form.customerId }}</code>
            </p>
          </div>
          <div class="definitions__field">
            <label :for="createFieldId('cluster')">{{ t('definitions.create.cluster') }}</label>
            <select
              :id="createFieldId('cluster')"
              v-model="form.clusterId"
              name="create-clusterId"
              :disabled="!form.customerId || loadingClusters"
            >
              <option value="">
                {{ !form.customerId ? t('picker.pickCustomerFirst') : loadingClusters ? t('picker.loading') : t('picker.clusterPlaceholder') }}
              </option>
              <option v-for="cluster in clusters" :key="cluster.id" :value="cluster.id" :title="cluster.id">
                {{ clusterLabel(cluster) }}
              </option>
            </select>
            <p v-if="clustersError" class="definitions__error" role="alert">{{ clustersError }}</p>
            <p v-else-if="form.clusterId" class="definitions__picked">
              {{ t('picker.idLabel') }}<code>{{ form.clusterId }}</code>
            </p>
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

.definitions__basics input,
.definitions__basics select {
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

/* The picker shows the opaque id next to the human label so an operator can still
   recognise/copy the value the API uses. */
.definitions__picked {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}

.definitions__picked code {
  font-family: var(--font-family-mono, monospace);
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
