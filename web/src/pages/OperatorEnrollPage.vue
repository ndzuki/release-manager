<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, shallowRef, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import AppDialog from '@/components/common/AppDialog.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import FormField from '@/components/common/FormField.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import EnrollmentTokenModal from '@/components/operators/EnrollmentTokenModal.vue';
import PendingTokenPanel from '@/components/operators/PendingTokenPanel.vue';
import { useAuthStore } from '@/stores/auth';
import { useOperatorStore } from '@/stores/operator';

const route = useRoute();
const router = useRouter();
const store = useOperatorStore();
const auth = useAuthStore();
const customerId = computed(() => String(route.params.customerId));
const clusterId = computed(() => String(route.params.clusterId));
const showTokenModal = shallowRef(false);
const replacePendingToken = shallowRef(false);
const pendingLoading = shallowRef(false);

/*
 * TASK-275: both field errors used to render INSIDE the wrapping <label>, so their text
 * was part of the control's accessible NAME and a describedby pointing at the same node
 * would have announced it twice. FormField (TASK-268) owns the clean shape; the page
 * hands it the violation for each field. The server/local validator emits at most one
 * violation per field, so taking the first is lossless.
 */
const operatorNameError = computed(
  () => store.error?.fieldViolations?.find((item) => item.field === 'operatorName')?.description ?? '',
);
const ttlMinutesError = computed(
  () => store.error?.fieldViolations?.find((item) => item.field === 'ttlMinutes')?.description ?? '',
);

async function loadPending(): Promise<void> {
  pendingLoading.value = true;
  try {
    await store.loadPending(customerId.value, clusterId.value);
  } finally {
    pendingLoading.value = false;
  }
}

function openGenerate(replace = false): void {
  replacePendingToken.value = replace;
  showTokenModal.value = true;
}

// TASK-281: replacement and revocation confirm through the shared AppDialog
// primitive instead of `window.confirm`. Cancelling runs neither original path;
// confirming runs exactly what the confirm branch used to run.
const replaceDialogOpen = shallowRef(false);
const discardDialogOpen = shallowRef(false);

function requestReplace(): void {
  replaceDialogOpen.value = true;
}

function requestDiscard(): void {
  discardDialogOpen.value = true;
}

function confirmReplace(): void {
  replaceDialogOpen.value = false;
  openGenerate(true);
}

async function confirmDiscard(): Promise<void> {
  discardDialogOpen.value = false;
  await store.discardPending(customerId.value, clusterId.value);
}

async function closeModal(): Promise<void> {
  showTokenModal.value = false;
  replacePendingToken.value = false;
  store.resetEnrollmentForm();
  await loadPending();
}

watch([customerId, clusterId], async () => {
  showTokenModal.value = false;
  replacePendingToken.value = false;
  store.resetEnrollmentState();
  await loadPending();
}, { immediate: true });
</script>

<template>
  <section class="page">
    <header class="page__header">
      <div>
        <p class="eyebrow">{{ t('operator.enroll.eyebrow') }}</p>
        <h1>{{ t('operator.enroll.title') }}</h1>
        <p>{{ t('operator.enroll.note') }}</p>
      </div>
      <button type="button" @click="router.push({ name: 'OperatorList', params: { customerId, clusterId } })">{{ t('operator.enroll.back') }}</button>
    </header>

    <ForbiddenState v-if="!auth.canEnrollOperators || store.forbidden" />
    <LoadingState v-else-if="pendingLoading" :message="t('operator.enroll.checking')" />
    <ErrorState v-else-if="store.error && !store.pending" :message="store.error.message">
      <button type="button" @click="loadPending">{{ t('action.retry') }}</button>
    </ErrorState>

    <template v-else>
      <PendingTokenPanel
        v-if="store.pending?.state === 'pending'"
        :pending="store.pending"
        :can-manage="auth.canEnrollOperators"
        :busy="store.saving"
        @replace="requestReplace"
        @discard="requestDiscard"
      />

      <form class="card" @submit.prevent="openGenerate(false)">
        <h2>{{ t('operator.enroll.parameters') }}</h2>
        <!--
         TASK-275: the error is FormField's child (outside `label for`); the hint moved
         from an in-label <small> to FormField's help, which is described too.
        -->
        <FormField
          :label="t('operator.enroll.name')"
          :help="t('operator.enroll.nameHint')"
          :error="operatorNameError"
        >
          <template #default="{ id, describedBy, invalid, disabled }">
            <input
              :id="id"
              v-model="store.enrollmentForm.operatorName"
              type="text"
              maxlength="63"
              autocomplete="off"
              placeholder="operator-staging"
              :aria-describedby="describedBy"
              :aria-invalid="invalid"
              :disabled="disabled"
            />
          </template>
        </FormField>
        <FormField
          :label="t('operator.enroll.ttl')"
          :help="t('operator.enroll.ttlHint')"
          :error="ttlMinutesError"
        >
          <template #default="{ id, describedBy, invalid, disabled }">
            <input
              :id="id"
              v-model.number="store.enrollmentForm.ttlMinutes"
              type="number"
              min="0"
              max="1440"
              :aria-describedby="describedBy"
              :aria-invalid="invalid"
              :disabled="disabled"
            />
          </template>
        </FormField>
        <p v-if="store.error" class="error" role="alert">{{ store.error.message }}</p>
        <button type="submit" class="primary" :disabled="store.saving || store.pending?.state === 'pending'">
          {{ t('operator.enroll.submit') }}
        </button>
        <p v-if="store.pending?.state === 'pending'" class="hint">{{ t('operator.enroll.pendingBlocked') }}</p>
      </form>
    </template>

    <AppDialog
      :open="replaceDialogOpen"
      :title="t('operator.enroll.replaceTitle')"
      :description="t('operator.enroll.replaceConfirm')"
      danger
      :close-on-backdrop="false"
      @close="replaceDialogOpen = false"
    >
      <template #footer>
        <button type="button" @click="replaceDialogOpen = false">{{ t('action.cancel') }}</button>
        <button type="button" class="dialog-danger" @click="confirmReplace">{{ t('operator.enroll.replaceAction') }}</button>
      </template>
    </AppDialog>

    <AppDialog
      :open="discardDialogOpen"
      :title="t('operator.enroll.revokeTitle')"
      :description="t('operator.enroll.revokeConfirm')"
      danger
      :close-on-backdrop="false"
      @close="discardDialogOpen = false"
    >
      <template #footer>
        <button type="button" @click="discardDialogOpen = false">{{ t('action.cancel') }}</button>
        <button type="button" class="dialog-danger" @click="confirmDiscard">{{ t('operator.enroll.revokeAction') }}</button>
      </template>
    </AppDialog>

    <EnrollmentTokenModal
      v-if="showTokenModal"
      :customer-id="customerId"
      :cluster-id="clusterId"
      :replace-pending-token="replacePendingToken"
      @close="closeModal"
    />
  </section>
</template>

<style scoped>
.page { display: grid; gap: 1.5rem; max-width: 54rem; margin: 0 auto; }
.page__header { display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
h1, h2, p { margin: 0; }
.page__header > div > p:last-child { margin-top: 0.375rem; color: var(--color-muted); }
.eyebrow { color: var(--color-primary); font-size: var(--font-size-xs); font-weight: 800; text-transform: uppercase; }
.page__header button, .card button { padding: 0.6rem 0.85rem; border: 1px solid var(--color-subtle); border-radius: 0.375rem; background: var(--color-surface); cursor: pointer; }
.card { display: grid; gap: 1rem; padding: 1.25rem; border: 1px solid var(--color-border-strong); border-radius: 0.75rem; }
/* TASK-275: the label/hint chrome now belongs to FormField; only the control is styled here. */
.card input { padding: 0.65rem; border: 1px solid var(--color-subtle); border-radius: 0.375rem; font: inherit; }
.hint { color: var(--color-muted); font-weight: 400; }
.card .primary { border-color: var(--color-primary); background: var(--color-primary); color: var(--color-on-accent); }
.card button:disabled { cursor: not-allowed; opacity: 0.5; }
.error { color: var(--color-error); }
/* Slotted into AppDialog's footer, whose baseline must lose to a danger variant. */
.dialog-danger { border-color: var(--color-danger-border); color: var(--color-error); }
</style>
