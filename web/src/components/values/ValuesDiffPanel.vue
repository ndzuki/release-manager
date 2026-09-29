<script setup lang="ts">
import { t } from '@/i18n/messages';
import type { DiffResult } from '@/types/valuesRevision';

const props = defineProps<{
  result: DiffResult;
}>();

function formatValue(value: unknown): string {
  if (value === undefined) return '—';
  return typeof value === 'string' ? value : JSON.stringify(value, null, 2);
}

function kindLabel(kind: DiffResult['changes'][number]['kind']): string {
  return {
    added: t('values.diff.added'),
    removed: t('values.diff.removed'),
    modified: t('values.diff.modified'),
    array_change: t('values.diff.arrayChanged'),
  }[kind];
}
</script>

<template>
  <section class="diff-panel" aria-labelledby="values-diff-title">
    <header class="diff-panel__header">
      <div>
        <p class="eyebrow">{{ t('values.diff.title') }}</p>
        <h2 id="values-diff-title">{{ t('values.diff.scope') }}</h2>
      </div>
      <span class="diff-panel__count">{{ props.result.changes.length }} {{ t('values.diff.changes') }}</span>
    </header>

    <div v-if="!props.result.hasChanges" class="diff-panel__empty" role="status">
      无 canonical 变化。格式、注释与 key 顺序差异不会产生无意义 diff。
    </div>
    <ol v-else class="diff-list">
      <li v-for="change in props.result.changes" :key="`${change.kind}:${change.path}`" class="diff-item">
        <div class="diff-item__summary">
          <span :class="['diff-kind', `diff-kind--${change.kind}`]">{{ kindLabel(change.kind) }}</span>
          <code>{{ change.path }}</code>
        </div>
        <div class="diff-item__values">
          <div v-if="change.oldValue !== undefined">
            <span>{{ t('values.diff.before') }}</span>
            <pre>{{ formatValue(change.oldValue) }}</pre>
          </div>
          <div v-if="change.newValue !== undefined">
            <span>{{ t('values.diff.after') }}</span>
            <pre>{{ formatValue(change.newValue) }}</pre>
          </div>
        </div>
      </li>
    </ol>
  </section>
</template>

<style scoped>
.diff-panel { display: grid; gap: 1rem; min-height: 30rem; padding: 1rem; border: 1px solid var(--color-border); border-radius: 0.75rem; background: var(--color-surface); }
.diff-panel__header { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
.diff-panel__header h2, .diff-panel__header p { margin: 0; }
.eyebrow { color: var(--color-primary); font-size: var(--font-size-xs); font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.diff-panel__count { color: var(--color-muted); font-size: var(--font-size-sm); }
.diff-panel__empty { display: grid; min-height: 20rem; place-items: center; padding: 1rem; color: var(--color-muted); text-align: center; }
.diff-list { display: grid; gap: 0.75rem; margin: 0; padding: 0; list-style: none; }
.diff-item { display: grid; gap: 0.7rem; padding: 0.8rem; border: 1px solid var(--color-border); border-radius: 0.6rem; }
.diff-item__summary { display: flex; align-items: center; gap: 0.65rem; }
.diff-item__summary code { color: var(--color-text-secondary); overflow-wrap: anywhere; }
.diff-kind { padding: 0.15rem 0.4rem; border-radius: 999px; font-size: var(--font-size-xs); font-weight: 800; text-transform: uppercase; }
.diff-kind--added { background: var(--color-success-surface); color: var(--color-success-ink); }
.diff-kind--removed { background: var(--color-danger-surface); color: var(--color-error-strong); }
.diff-kind--modified, .diff-kind--array_change { background: var(--color-warning-subtle); color: var(--color-warning-ink-strong); }
.diff-item__values { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.6rem; }
.diff-item__values span { color: var(--color-muted); font-size: var(--font-size-xs); font-weight: 700; text-transform: uppercase; }
pre { max-height: 12rem; margin: 0.25rem 0 0; padding: 0.65rem; overflow: auto; border-radius: 0.45rem; background: var(--color-bg); color: var(--color-text-secondary); font-size: var(--font-size-xs); white-space: pre-wrap; }
@media (max-width: 48rem) { .diff-item__values { grid-template-columns: 1fr; } }
</style>
