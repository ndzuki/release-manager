<script setup lang="ts">
import { computed } from 'vue';
import { t } from '@/i18n/messages';

/*
 * The one pagination primitive (ux-revamp-plan §9.2, ADR-029 clause 3; TASK-283).
 *
 * It exists because every list that pages writes the same two controls by hand:
 * OperationCenterPage (TASK-277) and AuditEventTable both carry their own buttons,
 * their own disabled logic and their own aria-label today, so the accessible name,
 * the wording and the disable rule drift page by page. DataTable is the table
 * primitive and owns sorting plus the async states — it has NO paging concept and a
 * cursor source is not a row source, so paging is a sibling primitive rather than a
 * DataTable prop (see TASK-283 for the full tradeoff).
 *
 * The contract, and what makes it the pager rather than a pair of buttons:
 *   - `hasPrev` / `hasNext` are the CALLER's reachability, not the source's state.
 *     A cursor feed folds "loading" in (`:has-prev="hasPrev && !loading"`), because
 *     this component cannot know whether a page is in flight.
 *   - `disabled` is the NATIVE attribute, never `aria-disabled`: assistive tech and
 *     the browser's own activation blocking read the same property, and a control
 *     that only LOOKS disabled still fires and can advance a cursor twice.
 *   - the click guard is not redundant with `disabled`. Native `disabled` blocks a
 *     real browser's activation, but a synthetic event (and happy-dom) still reaches
 *     the listener, so the guard is what makes "a disabled button never emits" hold
 *     on every input path.
 *   - the component never touches focus: mounting it, or flipping a prop, must leave
 *     the user's focus where it is. The buttons are native and in DOM order, so the
 *     browser's own tab order is already the correct one.
 *   - the `<nav>` carries the accessible name. Without it the region is announced as
 *     an unnamed navigation landmark, which is what a screen reader user pages past.
 */
interface Props {
  hasPrev: boolean;
  hasNext: boolean;
  /**
   * 1-based page index, shown as "第 N 页". Omit it when the source has no index to
   * display (a keyset cursor feed whose position is only "before/after").
   */
  page?: number;
  /** Accessible name for the `<nav>`; defaults to the generic catalog label. */
  label?: string;
}

const props = withDefaults(defineProps<Props>(), {
  page: undefined,
  label: undefined,
});

const emit = defineEmits<{
  prev: [];
  next: [];
}>();

const ariaLabel = computed(() => props.label ?? t('pagination.label'));

/** Empty while `page` is absent, which is also what gates the indicator's render. */
const pageLabel = computed(() => {
  const page = props.page;
  return page === undefined ? '' : t('pagination.page', { page });
});

function goPrev(): void {
  if (!props.hasPrev) return;
  emit('prev');
}

function goNext(): void {
  if (!props.hasNext) return;
  emit('next');
}
</script>

<template>
  <nav class="pagination" :aria-label="ariaLabel">
    <button type="button" class="pagination__button" :disabled="!hasPrev" @click="goPrev">
      {{ t('pagination.previous') }}
    </button>
    <p v-if="pageLabel" class="pagination__page" role="status">{{ pageLabel }}</p>
    <button type="button" class="pagination__button" :disabled="!hasNext" @click="goNext">
      {{ t('pagination.next') }}
    </button>
  </nav>
</template>

<style scoped>
.pagination {
  display: flex;
  gap: var(--space-3);
  align-items: center;
}

.pagination__button {
  padding: var(--space-2) var(--space-4);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  font: inherit;
  cursor: pointer;
}

.pagination__button:disabled {
  color: var(--color-subtle);
  cursor: not-allowed;
}

.pagination__page {
  margin: 0;
  color: var(--color-muted);
  font-size: var(--font-size-sm);
}
</style>
