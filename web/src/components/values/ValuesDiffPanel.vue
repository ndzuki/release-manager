<script setup lang="ts">
import { t } from '@/i18n/messages';
import { computed } from 'vue';
import StatusTag from '@/components/common/StatusTag.vue';
import type { DiffChange, DiffResult } from '@/types/valuesRevision';

/*
 * TASK-394 (direction C): the diff sits BESIDE the editor, so it reads as a side-by-side
 * patch instead of a stack of cards. Every hunk renders two aligned lines: the parent side
 * (removed, `--er`) and the editor side (added, `--ok`). A hunk that only adds or only
 * removes leaves the other side explicitly empty rather than shifting the columns, which is
 * what made the old before/after stack ambiguous at a glance.
 *
 * The gutter numbers are ORDINALS WITHIN THE DIFF VIEW, not document line numbers: the store
 * hands this panel a canonical DiffResult (path/kind/old/new), which carries no source line.
 * They exist to make removal/addition rows countable and `--mu` keeps them quiet — inventing
 * a per-document line number would be a claim the data cannot back.
 */
const props = defineProps<{
  result: DiffResult;
}>();

function formatValue(value: unknown): string {
  if (value === undefined) return '—';
  return typeof value === 'string' ? value : JSON.stringify(value, null, 2);
}

function kindLabel(kind: DiffChange['kind']): string {
  return {
    added: t('values.diff.added'),
    removed: t('values.diff.removed'),
    modified: t('values.diff.modified'),
    array_change: t('values.diff.arrayChanged'),
  }[kind];
}

function valueLines(value: unknown): number {
  return formatValue(value).split('\n').length;
}

interface DiffSide {
  number: number;
  text: string;
}

interface DiffHunk {
  key: string;
  kind: DiffChange['kind'];
  path: string;
  before: DiffSide | null;
  after: DiffSide | null;
}

const hunks = computed<DiffHunk[]>(() => {
  let beforeLine = 1;
  let afterLine = 1;
  return props.result.changes.map((change, index) => {
    const hunk: DiffHunk = {
      key: `${index}:${change.kind}:${change.path}`,
      kind: change.kind,
      path: change.path,
      before: change.oldValue === undefined ? null : { number: beforeLine, text: formatValue(change.oldValue) },
      after: change.newValue === undefined ? null : { number: afterLine, text: formatValue(change.newValue) },
    };
    if (change.oldValue !== undefined) beforeLine += valueLines(change.oldValue);
    if (change.newValue !== undefined) afterLine += valueLines(change.newValue);
    return hunk;
  });
});

const changeCount = computed(() => props.result.changes.length);
// "N 处变更" next to the title; a zero diff is its own neutral state, not a warning. The
// unit comes from the catalog; the number is data, so the label is composed rather than
// stored as a second, interpolated entry.
const countLabel = computed(() =>
  changeCount.value === 0
    ? t('values.diff.none')
    : [String(changeCount.value), t('values.diff.changes')].join(' '),
);
const countTone = computed<'wn' | 'neutral'>(() => (props.result.hasChanges ? 'wn' : 'neutral'));
</script>

<template>
  <section class="diff-panel" aria-labelledby="values-diff-title">
    <header class="diff-panel__header">
      <div class="diff-panel__heading">
        <p class="eyebrow">{{ t('values.diff.title') }}</p>
        <div class="diff-panel__title-row">
          <h2 id="values-diff-title">{{ t('values.diff.heading') }}</h2>
          <StatusTag :tone="countTone" :label="countLabel" />
        </div>
        <p class="diff-panel__scope">{{ t('values.diff.scope') }}</p>
      </div>
    </header>

    <div v-if="!props.result.hasChanges" class="diff-panel__empty" role="status">
      无 canonical 变化。格式、注释与 key 顺序差异不会产生无意义 diff。
    </div>
    <ol v-else class="diff-hunks">
      <li v-for="hunk in hunks" :key="hunk.key" class="diff-hunk">
        <div class="diff-hunk__summary">
          <span :class="['diff-kind', `diff-kind--${hunk.kind}`]">{{ kindLabel(hunk.kind) }}</span>
          <code>{{ hunk.path }}</code>
        </div>
        <div class="diff-hunk__sides">
          <div class="diff-side">
            <span class="diff-side__label">{{ t('values.diff.before') }}</span>
            <div v-if="hunk.before" class="diff-line diff-line--removed">
              <span class="diff-line__number" aria-hidden="true">{{ hunk.before.number }}</span>
              <span class="diff-line__marker" aria-hidden="true">−</span>
              <pre class="diff-line__text">{{ hunk.before.text }}</pre>
            </div>
            <p v-else class="diff-side__empty" aria-hidden="true">—</p>
          </div>
          <div class="diff-side">
            <span class="diff-side__label">{{ t('values.diff.after') }}</span>
            <div v-if="hunk.after" class="diff-line diff-line--added">
              <span class="diff-line__number" aria-hidden="true">{{ hunk.after.number }}</span>
              <span class="diff-line__marker" aria-hidden="true">+</span>
              <pre class="diff-line__text">{{ hunk.after.text }}</pre>
            </div>
            <p v-else class="diff-side__empty" aria-hidden="true">—</p>
          </div>
        </div>
      </li>
    </ol>
  </section>
</template>

<style scoped>
/* The card matches the editor card beside it: same surface, radius and minimum height, so
   the two columns read as one row of panels rather than two unrelated boxes. */
.diff-panel { display: grid; align-content: start; gap: 1rem; min-height: 30rem; min-width: 0; padding: 1rem; border: 1px solid var(--bd); border-radius: 0.75rem; background: var(--sf); }
.diff-panel__header { display: flex; align-items: flex-start; justify-content: space-between; gap: 1rem; }
.diff-panel__heading { display: grid; gap: 0.3rem; min-width: 0; }
.diff-panel__heading h2, .diff-panel__heading p { margin: 0; }
.diff-panel__title-row { display: flex; flex-wrap: wrap; align-items: center; gap: 0.6rem; }
.diff-panel__title-row h2 { font-size: var(--font-size-lg); }
.diff-panel__scope { color: var(--mu); font-size: var(--font-size-sm); }
.eyebrow { color: var(--mu); font-size: var(--font-size-xs); font-weight: 800; letter-spacing: 0.08em; text-transform: uppercase; }
.diff-panel__empty { display: grid; min-height: 20rem; place-items: center; padding: 1rem; color: var(--mu); font-size: var(--font-size-sm); text-align: center; }
.diff-hunks { display: grid; gap: 0.75rem; margin: 0; padding: 0; list-style: none; }
.diff-hunk { display: grid; gap: 0.5rem; min-width: 0; padding: 0.75rem; border: 1px solid var(--bd); border-radius: 0.6rem; }
.diff-hunk__summary { display: flex; flex-wrap: wrap; align-items: center; gap: 0.55rem; }
.diff-hunk__summary code { min-width: 0; color: var(--color-text-secondary); font-size: var(--font-size-xs); overflow-wrap: anywhere; }
.diff-kind { padding: 0.15rem 0.4rem; border-radius: 999px; font-size: var(--font-size-xs); font-weight: 800; text-transform: uppercase; }
.diff-kind--added { background: var(--color-success-surface); color: var(--color-success-ink); }
.diff-kind--removed { background: var(--color-danger-surface); color: var(--color-error-strong); }
.diff-kind--modified, .diff-kind--array_change { background: var(--color-warning-subtle); color: var(--color-warning-ink-strong); }
.diff-hunk__sides { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.5rem; }
.diff-side { display: grid; align-content: start; gap: 0.25rem; min-width: 0; }
.diff-side__label { color: var(--mu); font-size: var(--font-size-xs); font-weight: 700; text-transform: uppercase; }
.diff-line { display: grid; grid-template-columns: auto auto minmax(0, 1fr); align-items: start; gap: 0.4rem; min-width: 0; padding: 0.35rem 0.45rem; border-left: 2px solid transparent; border-radius: 0.3rem; background: var(--bg); }
/* Removed lines read as `--er`, added lines as `--ok`, on `--bg`: the measured pairs clear
   4.5:1 in both themes (tokens.test.ts owns the palette; the numbers are in the task record).
   The tinted surfaces were rejected on purpose — a 10% tint drops light-theme --er to 4.13. */
.diff-line--removed { border-left-color: var(--er); color: var(--er); }
.diff-line--added { border-left-color: var(--ok); color: var(--ok); }
.diff-line__number { color: var(--mu); font-family: var(--font-family-mono); font-size: var(--font-size-xs); text-align: right; }
.diff-line__marker { font-family: var(--font-family-mono); font-size: var(--font-size-xs); font-weight: 800; }
.diff-line__text { max-height: 12rem; margin: 0; padding: 0; overflow: auto; background: transparent; color: inherit; font-family: var(--font-family-mono); font-size: var(--font-size-xs); white-space: pre-wrap; overflow-wrap: anywhere; }
.diff-side__empty { margin: 0; padding: 0.35rem 0.45rem; color: var(--mu); font-size: var(--font-size-xs); }
@media (max-width: 48rem) { .diff-hunk__sides { grid-template-columns: 1fr; } }
</style>
