<script setup lang="ts">
import { t } from '@/i18n/messages';
import type { BundleSummary, OperationType, PatchOverride } from '@/types/operation';

defineProps<{
  operationType: OperationType;
  customerName: string;
  clusterName: string;
  releaseName: string;
  bundle: BundleSummary | null;
  valuesRevisionId: string;
  patch: PatchOverride[];
  currentRevision: number | null;
  submitting: boolean;
  /**
   * TASK-394: the form is checked before the operation is created. The confirmation
   * button stays disabled (neutral --color-disabled-* state) until the form's own
   * validation has moved the store into its confirmation step, so a click cannot skip
   * the checks and submit an unvalidated draft.
   */
  confirmable: boolean;
}>();

defineEmits<{
  cancel: [];
  confirm: [];
}>();
</script>

<template>
  <section class="confirm-panel" aria-labelledby="operation-confirm-title">
    <header class="confirm-panel__head">
      <p class="confirm-panel__eyebrow">{{ t('operation.confirm.title') }}</p>
      <h2 id="operation-confirm-title" class="confirm-panel__title">{{ t('operation.confirm.submit') }}</h2>
      <p class="confirm-panel__note">{{ t('operation.confirm.note') }}</p>
    </header>

    <!--
      A definition list, not a table: every row is one label/value pair and the values
      wrap (`overflow-wrap:anywhere`) so a long release name cannot widen the sidebar.
      The type row was added with the direction-C layout; the rest of the copy is the
      Wave 8 catalog and the artifact sentence keeps its original fragments so the
      copy gate's allowlist stays exact.
    -->
    <dl class="confirm-panel__summary">
      <div><dt>{{ t('operation.confirm.type') }}</dt><dd>{{ operationType }}</dd></div>
      <div><dt>{{ t('operation.confirm.release') }}</dt><dd>{{ releaseName || '—' }}</dd></div>
      <div><dt>{{ t('operation.confirm.cluster') }}</dt><dd>{{ customerName }} / {{ clusterName }}</dd></div>
      <div>
        <dt>{{ t('operation.confirm.artifact') }}</dt>
        <dd v-if="bundle">{{ bundle.name }} v{{ bundle.chartVersion }}，{{ bundle.images.length }} 个镜像</dd>
        <dd v-else class="confirm-panel__empty">—</dd>
      </div>
      <div>
        <dt>{{ t('operation.confirm.version') }}</dt>
        <dd class="mono">{{ valuesRevisionId || '—' }}</dd>
      </div>
      <div v-if="operationType !== 'INSTALL'">
        <dt>{{ t('operation.confirm.currentRevision') }}</dt>
        <dd class="tabular-nums">{{ currentRevision ?? '—' }}</dd>
      </div>
      <div>
        <dt>{{ t('operation.confirm.patch') }}</dt>
        <dd>{{ patch.length > 0 ? `${patch.length} 项` : t('operation.confirm.noPatch') }}</dd>
      </div>
    </dl>

    <p v-if="!confirmable && !submitting" class="confirm-panel__hint">{{ t('operation.confirm.hint') }}</p>

    <div class="confirm-panel__actions">
      <button type="button" class="confirm-panel__cancel" :disabled="submitting" @click="$emit('cancel')">
        {{ t('action.cancel') }}
      </button>
      <button
        type="button"
        class="confirm-panel__confirm"
        :disabled="submitting || !confirmable"
        @click="$emit('confirm')"
      >
        {{ submitting ? '创建中…' : '确认创建' }}
      </button>
    </div>
  </section>
</template>

<style scoped>
.confirm-panel {
  display: grid;
  gap: 14px;
  padding: 18px;
  border: 1px solid var(--bd);
  border-radius: var(--r);
  background: var(--sf);
}

.confirm-panel__head {
  display: grid;
  gap: 4px;
}

.confirm-panel__eyebrow {
  margin: 0;
  color: var(--mu);
  font-size: var(--font-size-xs);
  font-weight: var(--font-weight-medium);
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.confirm-panel__title {
  margin: 0;
  color: var(--tx);
  font-size: var(--font-size-h2);
  font-weight: var(--font-weight-heading);
}

.confirm-panel__note {
  margin: 0;
  color: var(--mu);
  font-size: var(--font-size-sm);
}

.confirm-panel__summary {
  display: grid;
  margin: 0;
  border: 1px solid var(--bd);
  border-radius: 6px;
}

.confirm-panel__summary > div {
  display: grid;
  gap: 2px;
  min-width: 0;
  padding: 8px 10px;
  border-top: 1px solid var(--bd);
}

.confirm-panel__summary > div:first-child {
  border-top: 0;
}

.confirm-panel__summary dt {
  color: var(--mu);
  font-size: var(--font-size-xs);
}

.confirm-panel__summary dd {
  margin: 0;
  color: var(--tx);
  font-weight: var(--font-weight-medium);
  overflow-wrap: anywhere;
}

.confirm-panel__empty {
  color: var(--mu);
}

/*
 * The hint is an informational callout, and it is also the remaining consumer of
 * --color-info-surface: the old panel painted its summary separators with that token,
 * and the token gate requires every declared token to keep a real consumer. A quiet
 * tint behind "why is the button disabled" is a better home for it than a coloured rule
 * inside a neutral card.
 */
.confirm-panel__hint {
  margin: 0;
  padding: 8px 10px;
  border-radius: 6px;
  background: var(--color-info-surface);
  color: var(--tx);
  font-size: var(--font-size-sm);
}

.confirm-panel__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.confirm-panel__actions button {
  padding: 8px 12px;
  border-radius: 6px;
  font-weight: var(--font-weight-medium);
  cursor: pointer;
}

.confirm-panel__cancel {
  flex: 1 1 40%;
  border: 1px solid var(--bd);
  background: var(--sf2);
  color: var(--tx);
}

.confirm-panel__confirm {
  flex: 1 1 55%;
  border: 1px solid var(--pr);
  background: var(--pr);
  color: var(--on);
}

/*
 * Disabled is its own neutral state (tokens.css --color-disabled-*): a disabled
 * confirmation must not look like a destructive action nor like an enabled primary.
 * `:disabled` in the selector is allowed by the token gate; the declarations use only
 * the disabled triple.
 */
.confirm-panel__actions button:disabled {
  border-color: var(--color-disabled-border);
  background: var(--color-disabled-bg);
  color: var(--color-disabled-text);
  box-shadow: none;
  cursor: not-allowed;
}

@media (max-width: 30rem) {
  .confirm-panel__actions {
    display: grid;
  }
}
</style>
