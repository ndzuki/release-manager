<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { t } from '@/i18n/messages';

/*
 * TASK-299: the notification primitive the ad-hoc `role="status"` / `role="alert"`
 * paragraphs across the console migrate to. Two contract points are load-bearing and
 * non-obvious, so they are stated here rather than left to the call sites:
 *
 * 1. PERSISTENT BY DEFAULT. `autoDismissMs` is optional and its absence means "stay on
 *    screen until the caller removes it". The card that commissioned this primitive
 *    assumed the console already had auto-dismissing notices; reading the pages said
 *    otherwise — every existing notice is persistent, and the only two setTimeout calls
 *    are a simulated delay (ChangePasswordPage) and a search debounce
 *    (ReleaseInventoryPage). Defaulting to a timer would therefore be a behaviour
 *    change, so the timer runs only when a caller explicitly asks for it.
 *
 * 2. THE ROLE FOLLOWS THE TONE. An error is `alert` (assertive); success and plain
 *    information are `status` (polite). Downgrading an error to `status` removes the
 *    screen-reader priority that is the whole point of announcing a failure.
 *
 * The primitive renders ONE notification in normal document flow: several on a page
 * keep their order and stay distinguishable, which is the stacking the contract asks
 * for without a fixed-position overlay that would need a container of its own. The
 * caller controls the lifetime with `v-if` and keeps its own `data-testid`.
 */
type ToastTone = 'info' | 'success' | 'error';

interface ToastProps {
  /** `error` switches the live region to assertive; see note 2 above. */
  tone?: ToastTone;
  /** `data-testid` for the root; the dismiss control derives `${testid}-dismiss`. */
  testid?: string;
  /** Accessible name of the dismiss control (defaults to the catalog's copy). */
  label?: string;
  /** Absent by default: without an explicit value the toast never dismisses itself. */
  autoDismissMs?: number;
}

const props = withDefaults(defineProps<ToastProps>(), {
  tone: 'info',
  testid: undefined,
  label: undefined,
  autoDismissMs: undefined,
});

const emit = defineEmits<{ dismiss: [] }>();

const visible = ref(true);
const hovering = ref(false);
const focusWithin = ref(false);
const paused = computed(() => hovering.value || focusWithin.value);
const role = computed(() => (props.tone === 'error' ? 'alert' : 'status'));
const dismissLabel = computed(() => props.label ?? t('common.dismissNotice'));
const dismissTestId = computed(() => (props.testid === undefined ? undefined : `${props.testid}-dismiss`));

let timer: ReturnType<typeof setTimeout> | undefined;

function clearTimer(): void {
  if (timer === undefined) return;
  clearTimeout(timer);
  timer = undefined;
}

/*
 * Restart rather than resume when the pause ends: the reader gets the whole interval
 * back instead of the remainder of a countdown they may never have seen running. The
 * case that pins this down lets the interval partly elapse before the pointer arrives,
 * then leaves and checks the notice survives the remainder and goes after a full one.
 */
function armTimer(): void {
  clearTimer();
  if (!visible.value || paused.value || props.autoDismissMs === undefined) return;
  timer = setTimeout(() => {
    visible.value = false;
    emit('dismiss');
  }, props.autoDismissMs);
}

watch(() => props.autoDismissMs, armTimer);
watch(paused, (value) => {
  if (value) clearTimer();
  else armTimer();
});

onMounted(armTimer);
onBeforeUnmount(clearTimer);

/*
 * The stylesheet reads this preference too, and base.css carries a console-wide
 * fallback; reading it here as well gives the behaviour a seam a DOM test can falsify
 * (happy-dom does not evaluate media queries), and keeps a future non-CSS motion path
 * honouring the preference. Read once at mount: a later OS-level change is picked up
 * on the next mount.
 */
const reduceMotion = ref(
  typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches,
);

/* Dismissal never touches `document.activeElement`: the control is removed, not focused. */
function dismiss(): void {
  clearTimer();
  visible.value = false;
  emit('dismiss');
}
</script>

<template>
  <p
    v-if="visible"
    class="toast"
    :class="[`toast--${tone}`, { 'toast--reduced-motion': reduceMotion }]"
    :role="role"
    :data-testid="testid"
    @mouseenter="hovering = true"
    @mouseleave="hovering = false"
    @focusin="focusWithin = true"
    @focusout="focusWithin = false"
  >
    <span class="toast__message"><slot /></span>
    <button
      type="button"
      class="toast__dismiss"
      :aria-label="dismissLabel"
      :data-testid="dismissTestId"
      @click="dismiss"
    >
      <span aria-hidden="true">×</span>
    </button>
  </p>
</template>

<style scoped>
/*
 * Layout and motion stay on `.toast` (no caller sets these properties), while every
 * visual default sits in `:where()` so it scores (0,1,0) once scoped and a caller's own
 * class — `.definitions__notice`, for instance — always wins on the properties it sets.
 * A plain `.toast { background: … }` would tie with a scoped page class at (0,2,0) and
 * the winner would depend on stylesheet order.
 */
.toast {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: var(--space-3);
  animation: toast-enter 160ms ease-out;
}

:where(.toast) {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-info-border);
  border-radius: var(--radius-lg);
  background: var(--color-info-soft);
  color: var(--color-info-ink);
  font-size: var(--font-size-sm);
}

:where(.toast--success) {
  border-color: var(--color-success-border);
  background: var(--color-success-surface-soft);
  color: var(--color-success-ink);
}

:where(.toast--error) {
  border-color: var(--color-danger-border);
  background: var(--color-danger-soft);
  color: var(--color-error-strong);
}

.toast__message {
  margin: 0;
}

.toast__dismiss {
  flex: none;
  padding: 0 var(--space-1);
  border: 0;
  background: none;
  color: inherit;
  font: inherit;
  line-height: 1;
  cursor: pointer;
}

/*
 * `.toast.toast--reduced-motion`, not `:where(...)`: the base rule owns `animation` at
 * (0,2,0) once scoped, so a zero-specificity variant would be a dead rule and the entry
 * animation would still run. The stylesheet half of this is asserted in Toast.test.ts,
 * because happy-dom does not evaluate scoped CSS.
 */
.toast.toast--reduced-motion {
  animation: none;
}

@keyframes toast-enter {
  from {
    opacity: 0;
    transform: translateY(-0.25rem);
  }

  to {
    opacity: 1;
    transform: none;
  }
}

@media (prefers-reduced-motion: reduce) {
  .toast {
    animation: none;
  }
}
</style>
