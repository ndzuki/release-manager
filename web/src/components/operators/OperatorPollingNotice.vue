<script setup lang="ts">
import { t } from '@/i18n/messages';

/*
 * TASK-325 (AC-325-05): the visible half of `useOperatorPolling`'s `stopped` state.
 *
 * The composable's retry budget is bounded (MAX_CONSECUTIVE_REFRESH_FAILURES), so a
 * persistently failing endpoint does stop the automatic refresh — and until now nothing
 * on screen said so: the last good data kept rendering as if it were still being
 * updated. This notice states the stop and carries the restore path, `resume()`.
 *
 * `role="status"` (the polite live region), not `alert`: this is a lingering state of the
 * page rather than a new failure event, and the page's stale-data paragraph already
 * announces the error itself with `role="alert"` — two assertive announcements would read
 * the same failure twice. The button relies on the console-wide `:focus-visible` policy
 * in styles/base.css; it sets no outline of its own.
 *
 * TASK-330 (AC-330-03): the copy is deliberately scoped to the chain, never to data
 * freshness. `stopped` stays true after a successful out-of-band refresh — only
 * `resume()` or a focus/visibility refresh re-arms the chain — while the page's stale
 * paragraph (`role="alert"`) owns the freshness claim and does clear on that success.
 * A "the data may be stale" sentence here would therefore outlive the fact behind it.
 */
defineProps<{ stopped: boolean }>();

const emit = defineEmits<{ resume: [] }>();
</script>

<template>
  <p v-if="stopped" class="polling-stopped" role="status" data-testid="operator-polling-stopped">
    <span>{{ t('operator.polling.stopped') }}</span>
    <button
      type="button"
      class="polling-stopped__resume"
      data-testid="operator-polling-resume"
      @click="emit('resume')"
    >{{ t('operator.polling.resume') }}</button>
  </p>
</template>

<style scoped>
.polling-stopped {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  margin: 0;
  padding: 0.75rem;
  border-radius: 0.5rem;
  background: var(--color-warning-soft);
  color: var(--color-warning-ink);
}

.polling-stopped__resume {
  flex: none;
  padding: 0.4rem 0.7rem;
  border: 1px solid var(--color-subtle);
  border-radius: 0.375rem;
  background: var(--color-surface);
  color: inherit;
  font: inherit;
  cursor: pointer;
}
</style>
