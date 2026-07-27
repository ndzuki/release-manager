<script setup lang="ts">
import { computed, shallowRef, watch } from 'vue';
import { storeToRefs } from 'pinia';
import { useRoute, useRouter } from 'vue-router';
import ErrorState from '@/components/common/ErrorState.vue';
import ForbiddenState from '@/components/common/ForbiddenState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import OperatorStatusBadge from '@/components/operators/OperatorStatusBadge.vue';
import RevokeOperatorDialog from '@/components/operators/RevokeOperatorDialog.vue';
import { useOperatorPolling } from '@/composables/useOperatorPolling';
import { useAuthStore } from '@/stores/auth';
import { useOperatorStore } from '@/stores/operator';
import { formatOperatorTime, operatorSessionReasonLabel } from '@/utils/operator-format';

const route = useRoute();
const router = useRouter();
const store = useOperatorStore();
const auth = useAuthStore();
const customerId = computed(() => String(route.params.customerId));
const clusterId = computed(() => String(route.params.clusterId));
const { heartbeatIntervalSeconds } = storeToRefs(store);
const operatorId = computed(() => String(route.params.operatorId));
const showRevoke = shallowRef(false);

async function refresh(): Promise<boolean> {
  return store.loadDetail(customerId.value, clusterId.value, operatorId.value);
}

async function confirmRevoke(reason: string): Promise<void> {
  if (await store.revokeOperator(customerId.value, clusterId.value, operatorId.value, reason)) {
    showRevoke.value = false;
  }
}

watch([customerId, clusterId, operatorId], async () => {
  showRevoke.value = false;
  store.resetDetailState();
  await refresh();
}, { immediate: true });
useOperatorPolling({ heartbeatIntervalSeconds, refresh });
</script>

<template>
  <section class="page">
    <LoadingState v-if="store.loading && !store.current" message="Loading operator…" />
    <ForbiddenState v-else-if="store.forbidden" />
    <ErrorState
      v-else-if="store.notFound || (store.error && !store.current)"
      :title="store.notFound ? 'Operator not found' : 'Unable to load operator'"
      :message="store.error?.message"
    >
      <button type="button" @click="router.push({ name: 'OperatorList', params: { customerId, clusterId } })">Back to operators</button>
    </ErrorState>

    <template v-else-if="store.current">
      <header class="page__header">
        <div>
          <p class="eyebrow">Operator detail</p>
          <h1>{{ store.current.name || store.current.id }}</h1>
          <p>{{ store.current.id }}</p>
        </div>
        <div class="actions">
          <button type="button" @click="refresh">Refresh</button>
          <button
            v-if="auth.canRevokeOperators && store.current.lifecycleStatus !== 'revoked'"
            type="button"
            class="danger"
            @click="showRevoke = true"
          >Revoke operator</button>
        </div>
      </header>

      <p v-if="store.error" class="warning" role="alert">
        {{ store.error.message }} Existing data remains visible; use Refresh to retry.
      </p>

      <section class="card" aria-labelledby="status-title">
        <h2 id="status-title">Status</h2>
        <dl class="summary">
          <div><dt>Lifecycle</dt><dd><OperatorStatusBadge :lifecycle-status="store.current.lifecycleStatus" /></dd></div>
          <div><dt>Session</dt><dd><OperatorStatusBadge :session-status="store.current.sessionStatus" /></dd></div>
          <div><dt>Last heartbeat</dt><dd>{{ formatOperatorTime(store.current.lastHeartbeat) }}</dd></div>
          <div><dt>Registered</dt><dd>{{ formatOperatorTime(store.current.registeredAt) }}</dd></div>
        </dl>
        <p v-if="operatorSessionReasonLabel(store.current.sessionStatusReason)" class="reason">
          {{ operatorSessionReasonLabel(store.current.sessionStatusReason) }}
        </p>
      </section>

      <section class="card" aria-labelledby="identity-title">
        <h2 id="identity-title">Identity and runtime</h2>
        <dl class="details">
          <div><dt>Customer</dt><dd>{{ store.current.customerId }}</dd></div>
          <div><dt>Cluster</dt><dd>{{ store.current.clusterName }} · {{ store.current.clusterId }}</dd></div>
          <div><dt>Instance</dt><dd>{{ store.current.instanceId ?? 'Not reported' }}</dd></div>
          <div><dt>Version</dt><dd>{{ store.current.version ?? 'Not reported' }}</dd></div>
          <div><dt>Superseded by</dt><dd>{{ store.current.supersededBy ?? '—' }}</dd></div>
          <div><dt>Revoked at</dt><dd>{{ formatOperatorTime(store.current.revokedAt) }}</dd></div>
        </dl>
        <div v-if="Object.keys(store.current.capabilities).length" class="capabilities">
          <h3>Capabilities</h3>
          <ul>
            <li v-for="(value, key) in store.current.capabilities" :key="key"><strong>{{ key }}</strong>: {{ value }}</li>
          </ul>
        </div>
      </section>

      <section v-if="store.current.revokeReason" class="card danger-card" aria-labelledby="revoke-title">
        <h2 id="revoke-title">Revocation</h2>
        <p>{{ store.current.revokeReason }}</p>
      </section>
    </template>

    <RevokeOperatorDialog
      v-if="showRevoke && store.current"
      :operator-name="store.current.name"
      :submitting="store.saving"
      :error-message="store.error?.message"
      @cancel="showRevoke = false"
      @confirm="confirmRevoke"
    />
  </section>
</template>

<style scoped>
.page { display: grid; gap: 1.5rem; max-width: 72rem; margin: 0 auto; }
.page__header, .actions { display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
h1, h2, h3, p { margin: 0; }
.page__header > div:first-child > p:last-child { margin-top: 0.375rem; color: #64748b; }
.eyebrow { color: #2563eb; font-size: 0.75rem; font-weight: 800; text-transform: uppercase; }
.actions button { padding: 0.55rem 0.8rem; border: 1px solid #94a3b8; border-radius: 0.375rem; background: #fff; cursor: pointer; }
.actions .danger { border-color: #ef4444; color: #b91c1c; }
.warning { padding: 0.75rem; border-radius: 0.5rem; background: #fff7ed; color: #9a3412; }
.card { display: grid; gap: 1rem; padding: 1.25rem; border: 1px solid #cbd5e1; border-radius: 0.75rem; }
.summary, .details { display: grid; grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr)); gap: 1rem; margin: 0; }
dt { color: #64748b; }
dd { margin: 0.25rem 0 0; font-weight: 700; overflow-wrap: anywhere; }
.reason { padding: 0.75rem; border-radius: 0.5rem; background: #f8fafc; color: #475569; }
.capabilities { display: grid; gap: 0.5rem; }
.capabilities ul { margin: 0; padding-left: 1.25rem; }
.danger-card { border-color: #fecaca; background: #fef2f2; }
</style>
