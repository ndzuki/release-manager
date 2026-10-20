<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed, onMounted, ref } from 'vue';
import AppDialog from '@/components/common/AppDialog.vue';
import EmptyState from '@/components/common/EmptyState.vue';
import ErrorState from '@/components/common/ErrorState.vue';
import LoadingState from '@/components/common/LoadingState.vue';
import PageHeader from '@/components/common/PageHeader.vue';
import Toast from '@/components/common/Toast.vue';
import type { StuckLockView } from '@/connect/stuck-lock-api';
import { useAuthStore } from '@/stores/auth';
import { useStuckLocksStore } from '@/stores/stuckLocks';
import { formatTimestamp } from '@/utils/formatTimestamp';

/*
 * Emergency stuck locks (REQ-087, A10 of the UX plan).
 *
 * A stuck lock is a terminal EMERGENCY whose cluster effect is still UNKNOWN past
 * the observation window — it blocks the definition from accepting new work. The
 * release is irreversible and not replayable, so the dialog forces the operator to
 * choose a mode and justify it, and warns about what each mode records.
 *
 * TASK-394 (consistency wave 3): the header is the shared PageHeader (eyebrow "紧急"),
 * the page root dropped its own `--space-5` padding (the shell already pads 24px), the
 * filter card and the table frame use the direction-C tokens, and the visible copy
 * moved into the catalog. The radio VALUES stay inline: they are the wire enum, not copy.
 */
const store = useStuckLocksStore();
const auth = useAuthStore();

const target = ref<StuckLockView | null>(null);
const reason = ref('');
const evidence = ref('');
const mode = ref<'' | 'NOT_APPLIED_PROVEN' | 'AUDITED_OVERRIDE'>('');
const acknowledged = ref(false);
const validationError = ref('');

/** AppDialog renders this as the alertdialog heading, i.e. the accessible name. */
const dialogTitle = computed(() => (target.value ? t('stuck.dialog.title', { path: target.value.lockPathSummary }) : ''));
/*
 * The irreversibility warning rides on AppDialog's `description` rather than a
 * hand-written <p>: that is what associates it with the dialog via
 * `aria-describedby` (the hand-rolled block rendered it unassociated).
 */
const dialogDescription = computed(() => (target.value ? t('stuck.dialog.warning') : ''));

onMounted(() => {
  void store.load('');
});

function open(lock: StuckLockView): void {
  target.value = lock;
  reason.value = '';
  evidence.value = '';
  mode.value = '';
  acknowledged.value = false;
  validationError.value = '';
}

async function submit(): Promise<void> {
  if (!target.value) return;
  // Order matches the server (reason -> evidence -> mode) so the first message the
  // operator sees is the same one a direct API call would produce.
  const trimmed = reason.value.trim();
  if (!trimmed) {
    validationError.value = t('stuckLocks.validation.reasonRequired');
    return;
  }
  // The server counts RUNES, not bytes (1-1000 for the reason, <=500 for evidence).
  if ([...trimmed].length > 1000) {
    validationError.value = t('stuckLocks.validation.reasonTooLong');
    return;
  }
  if ([...evidence.value].length > 500) {
    validationError.value = t('stuckLocks.validation.evidenceTooLong');
    return;
  }
  if (!mode.value) {
    validationError.value = t('stuckLocks.validation.modeRequired');
    return;
  }
  if (!acknowledged.value) {
    validationError.value = t('stuckLocks.validation.ackRequired');
    return;
  }
  validationError.value = '';
  const ok = await store.release({
    intentId: target.value.intentId,
    reason: trimmed,
    mode: mode.value,
    evidence: evidence.value.trim() || undefined,
  });
  if (ok) target.value = null;
}

</script>

<template>
  <section class="stuck">
    <PageHeader
      :title="t('stuckLocks.page.title')"
      :eyebrow="t('stuck.eyebrow')"
      :subtitle="t('stuckLocks.page.subtitle')"
    >
      <template #actions>
        <button type="button" class="stuck__button" :disabled="store.loading" @click="store.load(store.definitionFilter)">
          {{ store.loading ? t('stuckLocks.refreshing') : t('action.refresh') }}
        </button>
      </template>
    </PageHeader>

    <form class="stuck__filter" @submit.prevent="store.load(store.definitionFilter)">
      <label>
        {{ t('stuckLocks.filter.label') }}
        <input v-model="store.definitionFilter" name="releaseDefinitionId" placeholder="Release Definition ID" />
      </label>
      <button type="submit" :disabled="store.loading">{{ t('stuckLocks.filter.submit') }}</button>
    </form>

    <!-- TASK-267: the release confirmation uses the shared Toast primitive. -->
    <Toast
      v-if="store.notice"
      class="stuck__notice"
      tone="success"
      testid="stuck-notice"
      @dismiss="store.notice = ''"
    >
      {{ store.notice }}
    </Toast>

    <ErrorState
      v-if="store.failure"
      :title="t('stuckLocks.load.failed')"
      :message="store.failure.message"
      :details="store.failure.details"
      :action-label="t('stuckLocks.load.failedAction')"
      @action="store.load(store.definitionFilter)"
    />

    <LoadingState v-if="store.loading && store.locks.length === 0" :message="t('stuckLocks.loading')" />
    <EmptyState
      v-else-if="store.isEmpty"
      :title="t('stuckLocks.empty.title')"
      :message="t('stuckLocks.empty.message')"
    />

    <div
      v-else-if="store.locks.length > 0"
      class="table-wrap stuck__table-wrap"
      role="region"
      :aria-label="t('stuckLocks.table.scrollRegion')"
      tabindex="0"
    >
      <table class="stuck__table">
      <thead>
        <tr>
          <th scope="col">{{ t('stuckLocks.table.definition') }}</th>
          <th scope="col">{{ t('stuckLocks.table.action') }}</th>
          <th scope="col">{{ t('stuckLocks.table.lockPath') }}</th>
          <th scope="col">{{ t('stuckLocks.table.terminal') }}</th>
          <th scope="col">{{ t('stuckLocks.table.window') }}</th>
          <th scope="col">{{ t('stuckLocks.table.actions') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="lock in store.locks" :key="lock.intentId" :data-testid="`stuck-lock-${lock.intentId}`">
          <td>
            <code>{{ lock.releaseDefinitionId }}</code>
            <br />
            <small>op {{ lock.operationId }}</small>
          </td>
          <td class="stuck__action">{{ lock.action }}</td>
          <td>{{ lock.lockPathSummary }}</td>
          <td>
            {{ formatTimestamp(lock.terminalAt) }}
            <br />
            <small>{{ t('stuckLocks.table.stuckSince', { time: formatTimestamp(lock.stuckSince) }) }}</small>
          </td>
          <td>{{ lock.observeTimeoutDisplay || '—' }}</td>
          <td>
            <button v-if="auth.canWrite" type="button" class="danger" :disabled="store.releasing" @click="open(lock)">{{ t('stuckLocks.release.entry') }}</button>
          </td>
        </tr>
      </tbody>
      </table>
    </div>

    <!-- Releasing a lock is irreversible and not replayable, so the confirmation
         goes through the shared primitive with `danger` (role=alertdialog) instead
         of a hand-rolled block: that adds aria-modal, the Tab trap, Escape and
         focus return. The warning is the dialog's `description`, so it is also the
         element `aria-describedby` points at. The dialog never closes itself; the
         owner clears `target`. -->
    <AppDialog
      v-if="target"
      :open="true"
      :title="dialogTitle"
      :description="dialogDescription"
      danger
      :close-on-backdrop="false"
      :close-on-escape="!store.releasing"
      @close="target = null"
    >
      <form class="stuck__form" @submit.prevent="submit">
        <fieldset>
          <legend>{{ t('stuckLocks.mode.legend') }}</legend>
          <label>
            <input v-model="mode" type="radio" name="mode" value="NOT_APPLIED_PROVEN" />
            {{ t('stuckLocks.mode.notApplied') }}
          </label>
          <label>
            <input v-model="mode" type="radio" name="mode" value="AUDITED_OVERRIDE" />
            {{ t('stuckLocks.mode.override') }}
          </label>
        </fieldset>
        <label>
          {{ t('stuckLocks.reason.label') }}
          <textarea v-model="reason" name="reason" rows="3" :placeholder="t('stuckLocks.reason.placeholder')"></textarea>
        </label>
        <label>
          {{ t('stuckLocks.evidence.label') }}
          <textarea v-model="evidence" name="evidence" rows="2" :placeholder="t('stuckLocks.evidence.placeholder')"></textarea>
        </label>

        <label class="stuck__ack">
          <input v-model="acknowledged" type="checkbox" name="acknowledged" />
          {{ t('stuckLocks.ack') }}
        </label>

        <p v-if="validationError" class="stuck__error" role="alert" data-testid="stuck-validation">{{ validationError }}</p>

        <div class="stuck__dialog-actions">
          <button type="button" :disabled="store.releasing" @click="target = null">{{ t('action.cancel') }}</button>
          <button type="submit" class="danger" :disabled="store.releasing" data-testid="stuck-submit">
            {{ store.releasing ? t('stuckLocks.submitting') : t('stuckLocks.submit') }}
          </button>
        </div>
      </form>
    </AppDialog>
  </section>
</template>

<style scoped>
.stuck {
  display: grid;
  gap: var(--space-4);
  min-width: 0;
}

.stuck__filter {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  align-items: flex-end;
  padding: var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
}

.stuck__filter label {
  display: grid;
  flex: 1;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
  font-weight: var(--font-weight-bold);
}

.stuck__filter input {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.stuck__notice {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-success-border);
  border-radius: var(--radius-lg);
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

/* Direction-C table frame. `.table-wrap` owns horizontal containment (the base rule),
   so the 6-column table scrolls inside its own box at 375px instead of widening the page. */
.stuck__table-wrap {
  border: 1px solid var(--color-border-strong);
  border-radius: var(--r);
  background: var(--sf);
}

.stuck__table {
  width: 100%;
  min-width: 56rem;
  border-collapse: collapse;
  background: var(--color-surface);
}

/* Quiet header band; the table used to share one th/td rule, so the header had no band. */
.stuck__table th {
  padding: 10px 20px;
  background: var(--color-surface-muted);
  color: var(--color-muted-strong);
  font-size: var(--font-size-xs);
  font-weight: 600;
  text-align: left;
  /* Without nowrap a 375px viewport breaks the header and the action name one glyph per
     line; the table scrolls inside `.table-wrap` instead. */
  white-space: nowrap;
}

.stuck__table td {
  padding: 10px 20px;
  border-top: 1px solid var(--color-border-strong);
  text-align: left;
}

.stuck__table small {
  color: var(--color-muted);
}

/* The action name is a short CJK phrase; left to wrap it shrinks to one glyph per line
   in the narrow viewport while the framed table scrolls anyway. */
.stuck__action {
  white-space: nowrap;
}

.stuck__table button,
.stuck__filter button,
.stuck__button,
.stuck__dialog-actions button {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  cursor: pointer;
}

.stuck__table button.danger,
.stuck__dialog-actions button.danger {
  border-color: var(--color-danger-border);
  color: var(--color-error-strong);
}

/* The confirmation body lives inside AppDialog, which owns the panel shell, the
   heading/description typography and the danger tone; only the form's own layout
   stays here (TASK-296). */
.stuck__form {
  display: grid;
  gap: var(--space-3);
}

.stuck__form fieldset {
  display: grid;
  gap: var(--space-2);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
}

.stuck__form label {
  display: grid;
  gap: var(--space-1);
  font-size: var(--font-size-sm);
}

.stuck__form textarea {
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
}

.stuck__ack {
  display: flex;
  gap: var(--space-2);
  align-items: flex-start;
  font-size: var(--font-size-sm);
}

.stuck__error {
  margin: 0;
  color: var(--color-error-strong);
  font-size: var(--font-size-sm);
}

.stuck__dialog-actions {
  display: flex;
  gap: var(--space-3);
  justify-content: flex-end;
}
</style>
