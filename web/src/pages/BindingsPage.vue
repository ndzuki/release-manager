<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, nextTick, onMounted, useTemplateRef } from 'vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import BindingCreateForm from '@/components/bindings/BindingCreateForm.vue';
import BindingRevokeDialog from '@/components/bindings/BindingRevokeDialog.vue';
import BindingTable from '@/components/bindings/BindingTable.vue';
import CapabilityGrantForm from '@/components/bindings/CapabilityGrantForm.vue';
import { useBindingCreate } from '@/composables/useBindingCreate';
import { useBindingRevoke } from '@/composables/useBindingRevoke';
import { useCapabilityGrant } from '@/composables/useCapabilityGrant';
import { useAuthStore } from '@/stores/auth';
import { useGovernanceStore } from '@/stores/governance';

/*
 * Bindings and capability grants (REQ-049 / REQ-027, A2 + A3 of the UX plan).
 *
 * The console could not bind an organization to a customer nor grant the
 * emergency-resolver capability, although both RPCs exist. Note the asymmetry the
 * contract forces: bindings are listable (revoked rows included, for history),
 * while capability grants have NO list RPC — the panel only applies a grant/revoke
 * and reports the source/policy versions the server answered.
 *
 * TASK-363: the page is a composition surface. The table, the confirmation and the
 * two forms live in components/bindings; the create/grant/revoke state lives in
 * composables. What stays here is the store wiring, the page-level states and the
 * focus hand-off after a successful revoke.
 */
const auth = useAuthStore();
const store = useGovernanceStore();

const activeOrgId = computed(() => auth.activeOrganization?.id ?? '');
const activeOrgName = computed(() => auth.activeOrganization?.name ?? '');
const activeBindings = computed(() => store.bindings.filter((binding) => binding.status === 'active'));

const {
  customerId: newCustomerId,
  error: createError,
  submit: submitBinding,
} = useBindingCreate({
  bind: (customerId) => store.bind(customerId),
  activeBindings: () => activeBindings.value,
});

const {
  subject,
  action,
  desiredState,
  error: grantError,
  submit: submitGrant,
} = useCapabilityGrant({
  grant: (target, capability, revoked) => store.setCapability(target, capability, revoked),
});

const {
  pending: pendingRevoke,
  failure: revokeFailure,
  description: revokeDescription,
  arm: armRevoke,
  close: closeRevoke,
  confirm,
} = useBindingRevoke({
  revoke: (binding) => store.revoke(binding),
  failureMessage: () => store.failure?.message ?? null,
});

/*
 * TASK-297: the row button is gone once the revocation lands, so the browser
 * would drop focus on <body>; the success notice is where the confirm left the
 * user, and `tabindex="-1"` is what lets us move focus there programmatically.
 */
const noticeRef = useTemplateRef<HTMLParagraphElement>('notice');

onMounted(() => {
  if (activeOrgId.value) void store.loadBindings(activeOrgId.value);
});

async function confirmRevoke(): Promise<void> {
  if (!(await confirm())) return;
  await nextTick();
  noticeRef.value?.focus();
}
</script>

<template>
  <section class="governance">
    <header class="governance__header">
      <div>
        <p class="eyebrow">{{ t('governance.eyebrow') }}</p>
        <h1>授权绑定</h1>
        <p class="governance__subtitle">
          {{ activeOrgName || '当前组织' }}
          <code v-if="activeOrgId">{{ activeOrgId }}</code>
        </p>
      </div>
      <button type="button" :disabled="store.loading" @click="store.loadBindings(activeOrgId)">
        {{ store.loading ? '刷新中…' : '刷新' }}
      </button>
    </header>

    <p
      v-if="store.notice"
      ref="notice"
      class="governance__notice"
      role="status"
      tabindex="-1"
      data-testid="governance-notice"
    >
      {{ store.notice }}
    </p>

    <ErrorState
      v-if="store.failure"
      title="操作未成功"
      :message="store.failure.message"
      :details="store.failure.details"
      :action-label="store.failure.retryable ? '重试' : ''"
      @action="store.loadBindings(activeOrgId)"
    />

    <LoadingState v-if="store.loading && store.bindings.length === 0" message="正在加载绑定…" />
    <EmptyState
      v-else-if="!store.loading && !store.failure && store.bindings.length === 0"
      title="暂无绑定"
      message="该组织还没有与任何客户建立授权绑定。"
    />

    <BindingTable
      v-else-if="store.bindings.length > 0"
      :bindings="store.bindings"
      :saving="store.saving"
      @revoke="armRevoke"
    />

    <!-- Rendered only while armed (the reference RevokeOperatorDialog pattern):
         `open` is always true and the owner decides when the dialog exists. -->
    <BindingRevokeDialog
      v-if="pendingRevoke"
      :description="revokeDescription"
      :failure="revokeFailure"
      :saving="store.saving"
      @close="closeRevoke"
      @confirm="confirmRevoke"
    />

    <div class="governance__panels">
      <BindingCreateForm
        v-model:customer-id="newCustomerId"
        :saving="store.saving"
        :error="createError"
        @submit="submitBinding"
      />

      <CapabilityGrantForm
        v-model:subject="subject"
        v-model:action="action"
        v-model:desired-state="desiredState"
        :saving="store.saving"
        :versions="store.grantVersions"
        :error="grantError"
        @submit="submitGrant"
      />
    </div>
  </section>
</template>

<style scoped>
.governance {
  display: grid;
  gap: var(--space-4);
  padding: var(--space-5);
}

.governance__header {
  display: flex;
  gap: var(--space-3);
  align-items: flex-start;
  justify-content: space-between;
}

.governance__header h1 {
  margin: 0;
  font-size: var(--font-size-xl);
}

.eyebrow {
  margin: 0;
  color: var(--color-primary);
  font-size: var(--font-size-xs);
  font-weight: 800;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.governance__subtitle {
  margin: var(--space-1) 0 0;
  color: var(--color-muted);
}

.governance__notice {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-success-border);
  border-radius: var(--radius-lg);
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

.governance__panels {
  display: grid;
  gap: var(--space-4);
  grid-template-columns: repeat(auto-fit, minmax(20rem, 1fr));
}
</style>
