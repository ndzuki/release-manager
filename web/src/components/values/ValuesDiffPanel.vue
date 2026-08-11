<script setup lang="ts">
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
    added: 'Added',
    removed: 'Removed',
    modified: 'Modified',
    array_change: 'Array changed',
  }[kind];
}
</script>

<template>
  <section class="diff-panel" aria-labelledby="values-diff-title">
    <header class="diff-panel__header">
      <div>
        <p class="eyebrow">Canonical diff</p>
        <h2 id="values-diff-title">Parent → Current</h2>
      </div>
      <span class="diff-panel__count">{{ props.result.changes.length }} changes</span>
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
            <span>Before</span>
            <pre>{{ formatValue(change.oldValue) }}</pre>
          </div>
          <div v-if="change.newValue !== undefined">
            <span>After</span>
            <pre>{{ formatValue(change.newValue) }}</pre>
          </div>
        </div>
      </li>
    </ol>
  </section>
</template>

<style scoped>
.diff-panel { display: grid; gap: 1rem; min-height: 30rem; padding: 1rem; border: 1px solid #e2e8f0; border-radius: 0.75rem; background: #fff; }
.diff-panel__header { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
.diff-panel__header h2, .diff-panel__header p { margin: 0; }
.eyebrow { color: #2563eb; font-size: 0.7rem; font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.diff-panel__count { color: #64748b; font-size: 0.8rem; }
.diff-panel__empty { display: grid; min-height: 20rem; place-items: center; padding: 1rem; color: #64748b; text-align: center; }
.diff-list { display: grid; gap: 0.75rem; margin: 0; padding: 0; list-style: none; }
.diff-item { display: grid; gap: 0.7rem; padding: 0.8rem; border: 1px solid #e2e8f0; border-radius: 0.6rem; }
.diff-item__summary { display: flex; align-items: center; gap: 0.65rem; }
.diff-item__summary code { color: #334155; overflow-wrap: anywhere; }
.diff-kind { padding: 0.15rem 0.4rem; border-radius: 999px; font-size: 0.65rem; font-weight: 800; text-transform: uppercase; }
.diff-kind--added { background: #dcfce7; color: #166534; }
.diff-kind--removed { background: #fee2e2; color: #991b1b; }
.diff-kind--modified, .diff-kind--array_change { background: #fef3c7; color: #92400e; }
.diff-item__values { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.6rem; }
.diff-item__values span { color: #64748b; font-size: 0.7rem; font-weight: 700; text-transform: uppercase; }
pre { max-height: 12rem; margin: 0.25rem 0 0; padding: 0.65rem; overflow: auto; border-radius: 0.45rem; background: #f8fafc; color: #334155; font-size: 0.75rem; white-space: pre-wrap; }
@media (max-width: 48rem) { .diff-item__values { grid-template-columns: 1fr; } }
</style>
