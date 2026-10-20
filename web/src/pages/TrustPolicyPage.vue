<script setup lang="ts">
import { t, type MessageKey } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';
import { useAuthStore } from '@/stores/auth';
import { computed, onMounted, ref } from 'vue';
import AppDialog from '@/components/common/AppDialog.vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import Toast from '@/components/common/Toast.vue';
import TrustRootEditor from '@/components/trust/TrustRootEditor.vue';
import { TRUST_ENVIRONMENTS, allowedActions, type TrustAction, type TrustRootView } from '@/connect/trust-api';
import { useTrustPolicyStore } from '@/stores/trustPolicy';
import { formatTimestamp } from '@/utils/formatTimestamp';

/*
 * Signature trust roots (REQ-012 / REQ-043, A5 of the UX plan).
 *
 * GetTrustPolicy returns the WHOLE snapshot — every root ever created, retired and
 * revoked ones included — plus the policy version and revocation epoch. Writes are
 * state-ordered, so each row offers only the actions the current state accepts and
 * the page re-reads after every refusal.
 *
 * TASK-394 (consistency wave 3): the header is the shared PageHeader (eyebrow "治理");
 * the environment selector and the policy meta line moved out of the prose paragraph
 * into a filter card, which is where a control belongs; the page root no longer adds
 * `--space-5` on top of the shell's 24px; the table has a frame and a direction-C
 * header band; and the visible copy moved into the catalog.
 */
const store = useTrustPolicyStore();
const auth = useAuthStore();
const pendingAction = ref<{ root: TrustRootView; action: TrustAction } | null>(null);
// Create / rotate share one editor panel: create has no target root, rotate pre-seeds
// the old root id so the request cannot rotate a different row than the one clicked.
const editorOpen = ref(false);
const rotateTarget = ref<TrustRootView | null>(null);

function openCreate(): void {
  rotateTarget.value = null;
  editorOpen.value = true;
}

function openRotate(root: TrustRootView): void {
  rotateTarget.value = root;
  editorOpen.value = true;
}

function closeEditor(): void {
  editorOpen.value = false;
  rotateTarget.value = null;
}

const environments = TRUST_ENVIRONMENTS;

onMounted(() => {
  void store.load(store.environment);
});

const liveCount = computed(() => store.liveRootCount);

// The labels are catalog keys rather than Chinese literals so the whole page reads from
// one place; `allowedActions` still owns WHICH actions a state offers.
const ACTION_LABELS: Record<TrustAction, MessageKey> = {
  end_grace: 'trust.action.endGrace',
  retire: 'trust.action.retire',
  revoke: 'trust.action.revoke',
};

/** Why an action is unavailable, so the tooltip never states a wrong reason. */
function disabledReason(root: TrustRootView, action: TrustAction): string | undefined {
  if (store.saving) return undefined; // in flight: not a policy reason
  if (!store.canApply(root, action)) return t('trust.disabled.lastRoot');
  return undefined;
}

function request(root: TrustRootView, action: TrustAction): void {
  if (!store.canApply(root, action)) return;
  if (action === 'revoke') {
    pendingAction.value = { root, action };
    return;
  }
  void store.apply(root, action);
}

async function confirm(): Promise<void> {
  const pending = pendingAction.value;
  if (!pending) return;
  await store.apply(pending.root, pending.action);
  pendingAction.value = null;
}

/** Revocation is irreversible; AppDialog renders this as the alertdialog heading. */
const confirmTitle = computed(() =>
  pendingAction.value ? t('trust.confirm.title', { keyId: pendingAction.value.root.keyId }) : '',
);

</script>

<template>
  <section class="trust">
    <PageHeader :title="t('trust.page.title')" :eyebrow="t('trust.eyebrow')">
      <template #actions>
        <button type="button" class="trust__button" :disabled="store.loading" @click="store.load(store.environment)">
          {{ store.loading ? t('trust.refreshing') : t('action.refresh') }}
        </button>
        <button
          v-if="auth.canManageTrustRoots"
          type="button"
          class="trust__button"
          :disabled="store.saving"
          data-testid="trust-create-entry"
          @click="openCreate"
        >
          {{ t('trust.create.entry') }}
        </button>
      </template>
    </PageHeader>

    <!-- The environment selector and the policy version live in a card: the hand-rolled
         header wrapped them in a <p> that doubled as prose, which left the control
         unlabelled and the page without a filter surface. -->
    <div class="trust__filters">
      <label for="trust-environment">{{ t('trust.filter.environment') }}</label>
      <select id="trust-environment" v-model="store.environment" name="environment" @change="store.load(store.environment)">
        <option v-for="env in environments" :key="env" :value="env">{{ env }}</option>
      </select>
      <span v-if="store.policy" class="trust__policy">
        {{ t('trust.filter.version') }} <strong>{{ store.policy.version }}</strong>
        {{ t('trust.revocationEpoch') }} <strong>{{ store.policy.revocationEpoch }}</strong>
        · {{ t('trust.filter.liveRoots') }} <strong>{{ liveCount }}</strong>
      </span>
    </div>

    <!-- TASK-267: trust-root write outcomes use the shared Toast primitive. -->
    <Toast
      v-if="store.notice"
      class="trust__notice"
      tone="success"
      testid="trust-notice"
      @dismiss="store.notice = ''"
    >
      {{ store.notice }}
    </Toast>

    <ErrorState
      v-if="store.failure"
      :title="t('trust.error.title')"
      :message="store.failure.message"
      :details="store.failure.details"
      :action-label="t('trust.error.action')"
      @action="store.load(store.environment)"
    />

    <TrustRootEditor
      v-if="editorOpen"
      :mode="rotateTarget ? 'rotate' : 'create'"
      :root="rotateTarget"
      @close="closeEditor"
    />

    <LoadingState v-if="store.loading && !store.policy" :message="t('trust.loading')" />
    <EmptyState
      v-else-if="store.isEmpty"
      :title="t('trust.empty.title')"
      :message="t('trust.empty.message')"
    />

    <div
      v-else-if="store.policy && store.policy.roots.length > 0"
      class="table-wrap trust__table-wrap"
      role="region"
      :aria-label="t('trust.table.scrollRegion')"
      tabindex="0"
    >
      <table class="trust__table">
      <thead>
        <tr>
          <th scope="col">{{ t('trust.keyId') }}</th>
          <th scope="col">{{ t('trust.table.state') }}</th>
          <th scope="col">{{ t('trust.issuerSubject') }}</th>
          <th scope="col">{{ t('trust.table.validity') }}</th>
          <th scope="col">{{ t('trust.table.actions') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="root in store.policy.roots" :key="root.id" :data-testid="`trust-root-${root.keyId}`">
          <td><code>{{ root.keyId }}</code></td>
          <td>
            <span class="trust__state" :class="`trust__state--${root.state}`">{{ statusLabel('trustRoot', root.state) }}</span>
          </td>
          <td>
            {{ root.issuer }}
            <br />
            <small>{{ root.subjectPattern }}</small>
          </td>
          <td>
            {{ formatTimestamp(root.validFrom) }}
            <br />
            <small>{{ t('trust.table.graceUntil', { time: formatTimestamp(root.graceUntil) }) }}</small>
          </td>
          <td v-if="auth.canManageTrustRoots" class="trust__actions">
            <button
              v-if="root.state === 'active'"
              type="button"
              :disabled="store.saving"
              :data-testid="`trust-rotate-${root.keyId}`"
              @click="openRotate(root)"
            >
              {{ t('trust.rotate.entry') }}
            </button>
            <button
              v-for="action in allowedActions(root.state)"
              :key="action"
              type="button"
              :class="{ danger: action === 'revoke' }"
              :disabled="store.saving || !store.canApply(root, action)"
              :title="disabledReason(root, action)"
              :data-testid="`trust-${action}-${root.keyId}`"
              @click="request(root, action)"
            >
              {{ t(ACTION_LABELS[action]) }}
            </button>
          </td>
        </tr>
      </tbody>
      </table>
    </div>

    <!-- Revocation cannot be undone, so it goes through the shared primitive with
         `danger` (role=alertdialog) instead of a hand-rolled block: that adds
         aria-modal, the Tab trap, Escape and focus return. The dialog never closes
         itself; the owner clears `pendingAction`. -->
    <AppDialog
      v-if="pendingAction"
      :open="true"
      :title="confirmTitle"
      danger
      :close-on-backdrop="false"
      :close-on-escape="!store.saving"
      @close="pendingAction = null"
    >
      <p>{{ t('trust.dialog.revokeBody') }}</p>
      <template #footer>
        <button type="button" :disabled="store.saving" @click="pendingAction = null">{{ t('action.cancel') }}</button>
        <button
          type="button"
          class="trust__confirm-danger"
          :disabled="store.saving"
          data-testid="trust-confirm-revoke"
          @click="confirm"
        >
          {{ store.saving ? t('trust.dialog.submitting') : t('trust.dialog.confirmRevoke') }}
        </button>
      </template>
    </AppDialog>
  </section>
</template>

<style scoped>
.trust {
  display: grid;
  gap: var(--space-4);
  min-width: 0;
}

/* Direction-C filter card: the environment control and the policy meta are one surface
   with a strong border (`--color-border` is a ~1.3:1 hairline on the dark background). */
.trust__filters {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  align-items: center;
  padding: var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}

.trust__filters label {
  font-weight: var(--font-weight-bold);
}

.trust__filters select {
  padding: var(--space-1) var(--space-2);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.trust__filters strong {
  color: var(--color-text);
}

.trust__policy {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  align-items: baseline;
}

.trust__notice {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-success-border);
  border-radius: var(--radius-lg);
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

/* Direction-C table frame: `.table-wrap` keeps the 5-column table inside the page at
   375px (overflow-x), and the strong border plus the surface make it a card. */
.trust__table-wrap {
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
}

.trust__table {
  width: 100%;
  min-width: 48rem;
  border-collapse: collapse;
  background: var(--color-surface);
}

/* Quiet header band; th and td used to share one rule, so the header had no band and
   relied on the UA's bold weight. */
.trust__table th {
  padding: 10px 20px;
  background: var(--color-surface-muted);
  color: var(--color-muted-strong);
  font-size: var(--font-size-xs);
  font-weight: 600;
  text-align: left;
  white-space: nowrap;
}

.trust__table td {
  padding: 10px 20px;
  border-top: 1px solid var(--color-border-strong);
  text-align: left;
}

.trust__table small {
  color: var(--color-muted);
}

.trust__state {
  padding: 0.1rem var(--space-2);
  border-radius: var(--radius-xl);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-bold);
}

.trust__state--active {
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

.trust__state--grace {
  background: var(--color-warning-surface);
  color: var(--color-warning-ink-strong);
}

.trust__state--retired,
.trust__state--revoked {
  background: var(--color-surface-muted);
  color: var(--color-muted);
}

.trust__actions {
  display: flex;
  gap: var(--space-2);
}

.trust__actions button,
.trust__button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
  white-space: nowrap;
}

.trust__actions button.danger {
  border-color: var(--color-danger-border);
  color: var(--color-error-strong);
}

/* The confirm button is slotted into AppDialog's footer, which only supplies a
   baseline; the danger tone is the caller's rule (same split as BindingsPage). */
.trust__confirm-danger {
  border-color: var(--color-danger-border);
  color: var(--color-error-strong);
}

.trust__actions button:disabled,
.trust__button:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}
</style>
