<script setup lang="ts">
import { t } from '@/i18n/messages';
// Frozen-intent confirmation dialog (plan v3 Step 4, AC-058-15/16/17):
// renders a read-only summary of the frozen intent, requires the explicit
// risk acceptance checkbox, and submits through the store seam. Closing the
// dialog keeps the page form and the frozen key (reopening the same intent
// reuses it — handled by the store).
import AppDialog from '@/components/common/AppDialog.vue';
import type { CandidateArtifactDisplay, WorkloadRefDisplay } from '@/features/emergency/model';

defineProps<{
  open: boolean;
  workload: WorkloadRefDisplay | null;
  container: string;
  artifact: CandidateArtifactDisplay | null;
  reason: string;
  policy: string;
  riskAccepted: boolean;
  submitting: boolean;
  error: { code: string; message: string } | null;
}>();

const emit = defineEmits<{
  cancel: [];
  confirm: [];
  'update:risk-accepted': [accepted: boolean];
}>();
</script>

<template>
  <!-- The command is delivered the moment this is confirmed, so the summary must
       not be dismissed by a stray backdrop click; Escape still cancels. -->
  <AppDialog
    :open="open"
:title="t('emergency.confirm.submit')"
    danger
    :close-on-backdrop="false"
    :close-on-escape="!submitting"
    @close="emit('cancel')"
  >
    <dl class="emergency-confirm__summary">
      <template v-if="workload">
        <dt>{{ t('emergency.confirm.target') }}</dt>
        <dd>{{ workload.kind }} {{ workload.namespace }}/{{ workload.name }}</dd>
      </template>
      <dt>{{ t('emergency.confirm.container') }}</dt>
      <dd>{{ container || '—' }}</dd>
      <dt>{{ t('emergency.confirm.artifact') }}</dt>
      <dd>{{ artifact ? `${artifact.repository}（${artifact.digest}）` : '—' }}</dd>
      <dt>{{ t('emergency.confirm.policy') }}</dt>
      <dd>{{ policy }}</dd>
      <dt>{{ t('emergency.confirm.reason') }}</dt>
      <dd class="emergency-confirm__reason">{{ reason }}</dd>
    </dl>
    <p class="emergency-confirm__hint">{{ t('emergency.confirm.warning') }}</p>
    <label class="emergency-confirm__risk">
      <input
        type="checkbox"
        :checked="riskAccepted"
        @change="emit('update:risk-accepted', ($event.target as HTMLInputElement).checked)"
      />
      {{ t('emergency.confirm.ack') }}
    </label>
    <p v-if="error" class="emergency-confirm__error" role="alert">{{ error.message }}</p>

    <template #footer>
      <button type="button" :disabled="submitting" @click="emit('cancel')">{{ t('emergency.confirm.cancel') }}</button>
      <button type="button" class="primary" :disabled="submitting || !riskAccepted" @click="emit('confirm')">
        {{ submitting ? '提交中…' : '确认提交' }}
      </button>
    </template>
  </AppDialog>
</template>

<style scoped>
.emergency-confirm__summary {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: var(--space-1) var(--space-3);
  margin: 0;
}

.emergency-confirm__summary dt {
  color: var(--color-muted);
}

.emergency-confirm__summary dd {
  margin: 0;
}

.emergency-confirm__reason {
  overflow-wrap: anywhere;
}

/* HEAD's .risk-hint set an amber-800 colour and 0.85rem; both are tokenised
   one-to-one (see the equivalence pins in src/styles/tokens.test.ts). */
.emergency-confirm__hint {
  margin: 0;
  color: var(--color-warning-ink-strong);
  font-size: var(--font-size-sm);
}

.emergency-confirm__risk {
  display: flex;
  gap: var(--space-2);
  align-items: flex-start;
}

.emergency-confirm__error {
  margin: 0;
  color: var(--color-error);
}

/* HEAD's primary button was borderless on a red-600 fill; the emergency
   confirmation therefore stays danger-toned rather than brand blue. */
.primary {
  border: 0;
  background: var(--color-danger);
  color: var(--color-on-accent);
}
</style>
