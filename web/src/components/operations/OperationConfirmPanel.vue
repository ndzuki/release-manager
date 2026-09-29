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
}>();

defineEmits<{
  cancel: [];
  confirm: [];
}>();
</script>

<template>
  <section class="confirm-panel" aria-labelledby="operation-confirm-title">
    <header>
      <p class="confirm-panel__eyebrow">{{ t('operation.confirm.title') }}</p>
      <h2 id="operation-confirm-title">{{ t('operation.confirm.submit') }} {{ operationType }}</h2>
      <p>{{ t('operation.confirm.note') }}</p>
    </header>
    <dl class="confirm-panel__summary">
      <div><dt>{{ t('operation.confirm.release') }}</dt><dd>{{ releaseName }}</dd></div>
      <div><dt>{{ t('operation.confirm.cluster') }}</dt><dd>{{ customerName }} / {{ clusterName }}</dd></div>
      <div v-if="bundle"><dt>{{ t('operation.confirm.artifact') }}</dt><dd>{{ bundle.name }} v{{ bundle.chartVersion }}，{{ bundle.images.length }} 个镜像</dd></div>
      <div><dt>{{ t('operation.confirm.version') }}</dt><dd>{{ valuesRevisionId || '—' }}</dd></div>
      <div><dt>{{ t('operation.confirm.patch') }}</dt><dd>{{ patch.length > 0 ? `${patch.length} 项` : '无' }}</dd></div>
      <div v-if="operationType !== 'INSTALL'"><dt>{{ t('operation.confirm.currentRevision') }}</dt><dd>{{ currentRevision ?? '—' }}</dd></div>
    </dl>
    <div class="confirm-panel__actions">
      <button type="button" :disabled="submitting" @click="$emit('cancel')">{{ t('action.cancel') }}</button>
      <button type="button" class="confirm-panel__confirm" :disabled="submitting" @click="$emit('confirm')">
        {{ submitting ? '创建中…' : '确认创建' }}
      </button>
    </div>
  </section>
</template>

<style scoped>
.confirm-panel { display: grid; gap: 1.25rem; padding: 1.5rem; border: 2px solid var(--color-primary); border-radius: 0.85rem; background: var(--color-info-soft); }
.confirm-panel header h2, .confirm-panel header p { margin: 0; }
.confirm-panel__eyebrow { color: var(--color-primary-hover); font-size: var(--font-size-xs); font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.confirm-panel__summary { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0; margin: 0; border: 1px solid var(--color-info-border-soft); border-radius: 0.65rem; background: var(--color-surface); }
.confirm-panel__summary div { padding: 0.85rem 1rem; border-bottom: 1px solid var(--color-info-surface); }
.confirm-panel__summary dt { color: var(--color-muted); font-size: var(--font-size-sm); }
.confirm-panel__summary dd { margin: 0.2rem 0 0; font-weight: 650; overflow-wrap: anywhere; }
.confirm-panel__actions { display: flex; justify-content: flex-end; gap: 0.75rem; }
.confirm-panel__actions button { padding: 0.65rem 1rem; border: 1px solid var(--color-subtle); border-radius: 0.4rem; background: var(--color-surface); }
.confirm-panel__confirm { border-color: var(--color-primary) !important; background: var(--color-primary) !important; color: var(--color-on-accent); }
@media (max-width: 42rem) { .confirm-panel__summary { grid-template-columns: 1fr; } }
</style>
