<script setup lang="ts">
import { t } from '@/i18n/messages';
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
    <LoadingState v-if="store.loading && !store.current" :message="t('operator.detail.loading')" />
    <ForbiddenState v-else-if="store.forbidden" />
    <ErrorState
      v-else-if="store.notFound || (store.error && !store.current)"
      :title="store.notFound ? t('operator.detail.notFound') : t('operator.detail.loadFailed')"
      :message="store.error?.message"
    >
      <button type="button" @click="router.push({ name: 'OperatorList', params: { customerId, clusterId } })">{{ t('operator.enroll.back') }}</button>
    </ErrorState>

    <template v-else-if="store.current">
      <header class="page__header">
        <div>
          <p class="eyebrow">{{ t('operator.detail.title') }}</p>
          <h1>{{ store.current.name || store.current.id }}</h1>
          <p>{{ store.current.id }}</p>
        </div>
        <div class="actions">
          <button type="button" @click="refresh">{{ t('action.refresh') }}</button>
          <button
            v-if="auth.canRevokeOperators && store.current.lifecycleStatus !== 'revoked'"
            type="button"
            class="danger"
            @click="showRevoke = true"
          >{{ t('operator.detail.revokeOperator') }}</button>
        </div>
      </header>

      <p v-if="store.error" class="warning" role="alert">
        {{ store.error.message }} {{ t('operator.detail.stale') }}
      </p>

      <section class="card" aria-labelledby="status-title">
        <h2 id="status-title">{{ t('operator.detail.status') }}</h2>
        <dl class="summary">
          <div><dt>{{ t('operator.filters.lifecycle') }}</dt><dd><OperatorStatusBadge :lifecycle-status="store.current.lifecycleStatus" /></dd></div>
          <div><dt>{{ t('operator.filters.session') }}</dt><dd><OperatorStatusBadge :session-status="store.current.sessionStatus" /></dd></div>
          <div><dt>{{ t('operator.table.lastHeartbeat') }}</dt><dd>{{ formatOperatorTime(store.current.lastHeartbeat) }}</dd></div>
          <div><dt>{{ t('operator.table.registered') }}</dt><dd>{{ formatOperatorTime(store.current.registeredAt) }}</dd></div>
        </dl>
        <p v-if="operatorSessionReasonLabel(store.current.sessionStatusReason)" class="reason">
          {{ operatorSessionReasonLabel(store.current.sessionStatusReason) }}
        </p>
      </section>

      <section class="card" aria-labelledby="identity-title">
        <h2 id="identity-title">{{ t('operator.detail.identity') }}</h2>
        <dl class="details">
          <div><dt>{{ t('operator.detail.customer') }}</dt><dd>{{ store.current.customerId }}</dd></div>
          <div><dt>{{ t('operator.detail.cluster') }}</dt><dd>{{ store.current.clusterName }} · {{ store.current.clusterId }}</dd></div>
          <div><dt>{{ t('operator.detail.instance') }}</dt><dd>{{ store.current.instanceId ?? t('operator.detail.notReported') }}</dd></div>
          <div><dt>{{ t('operator.detail.version') }}</dt><dd>{{ store.current.version ?? t('operator.detail.notReported') }}</dd></div>
          <div><dt>{{ t('operator.detail.supersededBy') }}</dt><dd>{{ store.current.supersededBy ?? '—' }}</dd></div>
          <div><dt>{{ t('operator.detail.revokedAt') }}</dt><dd>{{ formatOperatorTime(store.current.revokedAt) }}</dd></div>
        </dl>
        <div v-if="Object.keys(store.current.capabilities).length" class="capabilities">
          <h3>{{ t('operator.detail.capabilities') }}</h3>
          <ul>
            <li v-for="(value, key) in store.current.capabilities" :key="key"><strong>{{ key }}</strong>: {{ value }}</li>
          </ul>
        </div>
      </section>

      <section v-if="store.current.revokeReason" class="card danger-card" aria-labelledby="revoke-title">
        <h2 id="revoke-title">{{ t('operator.detail.revocation') }}</h2>
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
.page__header > div:first-child > p:last-child { margin-top: 0.375rem; color: var(--color-muted); }
.eyebrow { color: var(--color-primary); font-size: var(--font-size-xs); font-weight: 800; text-transform: uppercase; }
.actions button { padding: 0.55rem 0.8rem; border: 1px solid var(--color-subtle); border-radius: 0.375rem; background: var(--color-surface); cursor: pointer; }
.actions .danger { border-color: var(--color-danger-border); color: var(--color-error); }
.warning { padding: 0.75rem; border-radius: 0.5rem; background: var(--color-warning-soft); color: var(--color-warning-ink); }
.card { display: grid; gap: 1rem; padding: 1.25rem; border: 1px solid var(--color-border-strong); border-radius: 0.75rem; }
.summary, .details { display: grid; grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr)); gap: 1rem; margin: 0; }
dt { color: var(--color-muted); }
dd { margin: 0.25rem 0 0; font-weight: 700; overflow-wrap: anywhere; }
.reason { padding: 0.75rem; border-radius: 0.5rem; background: var(--color-bg); color: var(--color-muted-strong); }
.capabilities { display: grid; gap: 0.5rem; }
.capabilities ul { margin: 0; padding-left: 1.25rem; }
.danger-card { border-color: var(--color-danger-border-soft); background: var(--color-danger-soft); }
</style>
