<script setup lang="ts">
import { t } from '@/i18n/messages';
import { statusLabel as valuesStatusLabel } from '@/i18n/status-labels';
import { computed } from 'vue';
import type {ValuesRevision} from '@/types/valuesRevision';

// submitDisabled is optional so the component stays usable standalone; the store
// enforces the same rule authoritatively (a caller that forgets it cannot submit
// unsaved content anyway).
const props = withDefaults(defineProps<{
  revision: ValuesRevision | null;
  saving: boolean;
  approving: boolean;
  discarding: boolean;
  saveDisabled: boolean;
  /** UX-003: submitting requires a saved, valid draft. */
  submitDisabled?: boolean;
  canApprove: boolean;
  selfApproval: boolean;
  readOnly: boolean;
}>(), { submitDisabled: false });

const emit = defineEmits<{ save: []; submit: []; approve: []; reject: []; discard: [] }>();

const statusLabel = computed(() => valuesStatusLabel('valuesRevision', props.revision?.status ?? 'draft'));

function formatTimestamp(value?: string): string {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : '';
}
</script>

<template>
  <section class="revision-actions" aria-labelledby="revision-actions-title">
    <div>
      <p class="eyebrow">{{ t('values.revision.workflow') }}</p>
      <h2 id="revision-actions-title">{{ revision ? `Revision ${revision.revision}` : t('values.revision.new') }}</h2>
      <p v-if="revision" class="status-line">
        <span :class="['status', `status--${revision.status}`]">{{ statusLabel }}</span>
        <code>{{ revision.valuesDigest }}</code>
      </p>
      <p v-if="revision?.status === 'rejected' && revision.decidedAt" class="rejection">
        {{ t('values.revision.rejectTime') }} {{ formatTimestamp(revision.decidedAt) }}
      </p>
      <p v-if="selfApproval && revision?.status === 'pending_approval'" class="hint">不可审批自己创建的 Revision。</p>
    </div>

    <div class="revision-actions__buttons">
      <!-- Save is also the FIRST-revision action: with no revision yet the store
           creates one (stores/valuesEditor.ts save() → createValuesRevision with
           the parent anchor). Gating it on `revision.status === 'draft'` left the
           editor with no way to save at all — the page renders "创建首个配置
           Revision。" and a convergence session prepared from a pending task starts
           with no draft, so the cross-actor approval flow was unreachable from the
           UI (found by actually running web/e2e/emergency-smoke.spec.ts). -->
      <button
        v-if="!readOnly && (revision === null || revision.status === 'draft')"
        type="button"
        class="primary"
        :disabled="saveDisabled"
        @click="emit('save')"
      >
        {{ saving ? '保存中…' : '保存 Draft' }}
      </button>
      <button
        v-if="!readOnly && revision?.status === 'draft'"
        type="button"
        class="primary"
        :disabled="approving || submitDisabled"
        :title="submitDisabled ? '请先保存 Draft：提交的是已保存的 Revision' : undefined"
        @click="emit('submit')"
      >
        {{ approving ? t('values.revision.submitting') : t('values.revision.submit') }}
      </button>
      <button
        v-if="!readOnly && revision?.status === 'draft'"
        type="button"
        class="secondary"
        :disabled="discarding"
        @click="emit('discard')"
      >
        {{ discarding ? '丢弃中…' : t('values.revision.discard') }}
      </button>
      <template v-if="canApprove && revision?.status === 'pending_approval'">
        <button type="button" class="success" :disabled="approving" @click="emit('approve')">
          {{ approving ? '审批中…' : t('values.revision.approve') }}
        </button>
        <button type="button" class="danger" :disabled="approving" @click="emit('reject')">{{ t('values.revision.reject') }}</button>
      </template>
    </div>
  </section>
</template>

<style scoped>
.revision-actions { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; padding: 1rem; border: 1px solid var(--color-border); border-radius: 0.75rem; background: var(--color-surface); }
h2, p { margin: 0; }
.eyebrow { color: var(--color-primary); font-size: var(--font-size-xs); font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.status-line { display: flex; flex-wrap: wrap; align-items: center; gap: 0.6rem; margin-top: 0.45rem; }
.status-line code { color: var(--color-muted); font-size: var(--font-size-xs); overflow-wrap: anywhere; }
.status { padding: 0.2rem 0.45rem; border-radius: 999px; font-size: var(--font-size-xs); font-weight: 800; }
.status--draft { background: var(--color-info-subtle); color: var(--color-info-ink-strong); }
.status--pending_approval { background: var(--color-warning-subtle); color: var(--color-warning-ink-strong); }
.status--approved { background: var(--color-success-surface); color: var(--color-success-ink); }
.status--rejected { background: var(--color-danger-surface); color: var(--color-error-strong); }
.status--superseded { background: var(--color-border); color: var(--color-muted-strong); }
.rejection { margin-top: 0.5rem; color: var(--color-error-strong); font-size: var(--font-size-sm); }
.hint { margin-top: 0.5rem; color: var(--color-warning-ink-strong); font-size: var(--font-size-sm); }
.revision-actions__buttons { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 0.65rem; }
button { min-height: 2.5rem; padding: 0.5rem 0.8rem; border: 1px solid var(--color-border-strong); border-radius: 0.45rem; background: var(--color-surface); cursor: pointer; }
button.primary { border-color: var(--color-primary); background: var(--color-primary); color: var(--color-on-accent); }
button.secondary { border-color: var(--color-error); color: var(--color-error); }
button.success { border-color: var(--color-success); background: var(--color-success); color: var(--color-on-accent); }
button.danger { border-color: var(--color-error); color: var(--color-error); }
button:disabled { cursor: not-allowed; opacity: 0.6; }
@media (max-width: 48rem) { .revision-actions { flex-direction: column; } .revision-actions__buttons { justify-content: flex-start; } }
</style>
