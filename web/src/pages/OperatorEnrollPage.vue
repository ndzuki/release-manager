<script setup lang="ts">
import { computed, shallowRef, watch } from 'vue';
import { useRoute, useRouter } from 'vue-router';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
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

async function replacePending(): Promise<void> {
  if (window.confirm('Replacing the pending token immediately revokes the old token. Continue?')) {
    openGenerate(true);
  }
}

async function discardPending(): Promise<void> {
  if (!window.confirm('Revoke the pending enrollment token? This cannot be undone.')) return;
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
        <p class="eyebrow">Operator enrollment</p>
        <h1>Generate a one-time token</h1>
        <p>The plaintext is displayed only inside the next modal and cannot be recovered after closing.</p>
      </div>
      <button type="button" @click="router.push({ name: 'OperatorList', params: { customerId, clusterId } })">Back to operators</button>
    </header>

    <ForbiddenState v-if="!auth.canEnrollOperators || store.forbidden" />
    <LoadingState v-else-if="pendingLoading" message="Checking pending token status…" />
    <ErrorState v-else-if="store.error && !store.pending" :message="store.error.message">
      <button type="button" @click="loadPending">Retry</button>
    </ErrorState>

    <template v-else>
      <PendingTokenPanel
        v-if="store.pending?.state === 'pending'"
        :pending="store.pending"
        :can-manage="auth.canEnrollOperators"
        :busy="store.saving"
        @replace="replacePending"
        @discard="discardPending"
      />

      <form class="card" @submit.prevent="openGenerate(false)">
        <h2>Enrollment parameters</h2>
        <label>
          Operator name
          <input v-model="store.enrollmentForm.operatorName" type="text" maxlength="63" autocomplete="off" placeholder="operator-staging" />
          <small>Lowercase DNS-compatible label, 1–63 characters.</small>
          <span v-if="store.error?.fieldViolations?.find((item) => item.field === 'operatorName')" class="error">
            {{ store.error.fieldViolations.find((item) => item.field === 'operatorName')?.description }}
          </span>
        </label>
        <label>
          TTL minutes
          <input v-model.number="store.enrollmentForm.ttlMinutes" type="number" min="0" max="1440" />
          <small>0 uses the 60-minute default; otherwise use 5–1440.</small>
          <span v-if="store.error?.fieldViolations?.find((item) => item.field === 'ttlMinutes')" class="error">
            {{ store.error.fieldViolations.find((item) => item.field === 'ttlMinutes')?.description }}
          </span>
        </label>
        <p v-if="store.error" class="error" role="alert">{{ store.error.message }}</p>
        <button type="submit" class="primary" :disabled="store.saving || store.pending?.state === 'pending'">
          Generate enrollment token
        </button>
        <p v-if="store.pending?.state === 'pending'" class="hint">Revoke or explicitly replace the pending token before generating another.</p>
      </form>
    </template>

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
.page__header > div > p:last-child { margin-top: 0.375rem; color: #64748b; }
.eyebrow { color: #2563eb; font-size: 0.75rem; font-weight: 800; text-transform: uppercase; }
.page__header button, .card button { padding: 0.6rem 0.85rem; border: 1px solid #94a3b8; border-radius: 0.375rem; background: #fff; cursor: pointer; }
.card { display: grid; gap: 1rem; padding: 1.25rem; border: 1px solid #cbd5e1; border-radius: 0.75rem; }
.card label { display: grid; gap: 0.35rem; font-weight: 700; }
.card input { padding: 0.65rem; border: 1px solid #94a3b8; border-radius: 0.375rem; font: inherit; }
.card small, .hint { color: #64748b; font-weight: 400; }
.card .primary { border-color: #2563eb; background: #2563eb; color: #fff; }
.card button:disabled { cursor: not-allowed; opacity: 0.5; }
.error { color: #b91c1c; }
</style>
