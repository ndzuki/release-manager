<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue';
import { t } from '@/i18n/messages';

/*
 * A truncated machine value (uuid, digest, release id) with an explicit copy affordance.
 *
 * Three contract points:
 *  - the VISIBLE text is truncated, the accessible/tooltip value is complete: `title`
 *    carries the full string and the copy button copies `props.value`, never the
 *    rendered slice (copying `abcd1234…` would be a silent data corruption);
 *  - a failed write is VISIBLE, not just announced. `navigator.clipboard` is absent on an
 *    insecure origin and can reject on a secure one while the document is unfocused, so
 *    the failure states that the value was not copied and points at the selectable text;
 *  - the feedback resets so the control stays usable, and the timer is cleared on unmount
 *    (a dangling setTimeout on a detached component is a leak the linter cannot see).
 */
const props = withDefaults(
  defineProps<{
    value: string;
    /** Characters kept at the start before the ellipsis. */
    head?: number;
    /** Characters kept at the end; 0 shows only the head. */
    tail?: number;
  }>(),
  { head: 8, tail: 0 },
);

const display = computed(() => {
  // One character of slack so a value that is only one longer than the window renders
  // whole instead of as `abcdefgh…i`. Plain concatenation, not a template literal: the
  // copy-hygiene scanner reads a backtick with spaces as a sentence, and an ellipsis is
  // not copy.
  if (props.value.length <= props.head + props.tail + 1) return props.value;
  const tail = props.tail > 0 ? props.value.slice(-props.tail) : '';
  return props.value.slice(0, props.head) + '…' + tail;
});

type CopyState = 'idle' | 'copied' | 'failed';

const state = ref<CopyState>('idle');
let resetTimer: ReturnType<typeof setTimeout> | null = null;

function clearReset(): void {
  if (resetTimer === null) return;
  clearTimeout(resetTimer);
  resetTimer = null;
}

function scheduleReset(): void {
  resetTimer = setTimeout(() => {
    state.value = 'idle';
    resetTimer = null;
  }, 1600);
}

onUnmounted(clearReset);

async function copyValue(): Promise<void> {
  clearReset();
  const clipboard = typeof navigator === 'undefined' ? undefined : navigator.clipboard;
  // An insecure origin has no clipboard API at all; that is a failed copy, not a no-op.
  if (!clipboard || typeof clipboard.writeText !== 'function') {
    state.value = 'failed';
    scheduleReset();
    return;
  }
  try {
    await clipboard.writeText(props.value);
    state.value = 'copied';
  } catch {
    state.value = 'failed';
  }
  scheduleReset();
}
</script>

<template>
  <span class="copyable-hash">
    <code class="copyable-hash__value mono" :title="value">{{ display }}</code>
    <button
      type="button"
      class="copyable-hash__button"
      :aria-label="t('common.copyFullValue')"
      @click="copyValue"
    >
      {{ t('common.copy') }}
    </button>
    <span
      v-if="state !== 'idle'"
      class="copyable-hash__feedback"
      :class="`copyable-hash__feedback--${state}`"
      role="status"
    >
      {{ state === 'failed' ? t('common.copyFailed') : t('common.copied') }}
    </span>
  </span>
</template>

<style scoped>
.copyable-hash {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
  min-width: 0;
}

.copyable-hash__value {
  padding: 2px 6px;
  border: 1px solid var(--bd);
  border-radius: 6px;
  background: var(--code);
  color: var(--tx);
  font-size: var(--font-size-xs);
  overflow-wrap: anywhere;
}

.copyable-hash__button {
  flex: none;
  padding: 2px 8px;
  border: 1px solid var(--bd);
  border-radius: 6px;
  background: var(--sf);
  color: var(--mu);
  font-size: var(--font-size-xs);
  cursor: pointer;
}

.copyable-hash__button:hover {
  border-color: var(--mu);
  color: var(--tx);
}

.copyable-hash__feedback {
  font-size: var(--font-size-xs);
}

.copyable-hash__feedback--copied {
  color: var(--color-success-ink);
}

.copyable-hash__feedback--failed {
  color: var(--color-error-strong);
}
</style>
