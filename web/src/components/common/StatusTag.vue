<script setup lang="ts">
/*
 * Direction C status pill: a 6px dot plus a label on a 10% tint of its own tone. The
 * tone is carried by `color` and the tinted border/background/dot derive from
 * `currentColor` with color-mix, so a tone is one line instead of four hex literals and
 * the text itself stays on the same palette entry as the dot.
 *
 * `role="status"` is deliberate: a tag can change while a page is open (a release moves
 * from pending to online), and the polite live region is what announces that change. A
 * caller that renders a static label inside an already-announced region can pass
 * `ariaLabel` to give it a name that does not repeat the visible text.
 */
type StatusTone = 'ok' | 'wn' | 'er' | 'neutral' | 'info';

withDefaults(
  defineProps<{
    tone: StatusTone;
    label: string;
    /** Breathing dot for a state that is still in progress. */
    pulse?: boolean;
    /** Optional accessible name; the visible label is used when absent. */
    ariaLabel?: string;
  }>(),
  { pulse: false, ariaLabel: undefined },
);
</script>

<template>
  <span
    class="status-tag"
    :class="`status-tag--${tone}`"
    role="status"
    :aria-label="ariaLabel"
  >
    <span class="status-tag__dot" :class="{ 'status-tag__dot--pulse': pulse }" aria-hidden="true" />
    <span class="status-tag__label">{{ label }}</span>
  </span>
</template>

<style scoped>
.status-tag {
  display: inline-flex;
  gap: 6px;
  align-items: center;
  padding: 0 9px;
  border: 1px solid color-mix(in srgb, currentColor 28%, transparent);
  border-radius: 999px;
  background: color-mix(in srgb, currentColor 10%, transparent);
  font-size: var(--font-size-xs);
  line-height: 1.7;
  white-space: nowrap;
}

.status-tag__dot {
  position: relative;
  flex: none;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}

.status-tag__dot--pulse::after {
  position: absolute;
  inset: -3px;
  border-radius: 50%;
  background: currentColor;
  opacity: 0.35;
  animation: status-tag-pulse 1.8s ease-out infinite;
  content: '';
}

.status-tag--ok {
  color: var(--ok);
}

.status-tag--wn {
  color: var(--wn);
}

.status-tag--er {
  color: var(--er);
}

.status-tag--neutral {
  color: var(--mu);
}

.status-tag--info {
  color: var(--ac);
}

.status-tag__label {
  font-weight: var(--font-weight-medium);
}

@keyframes status-tag-pulse {
  0% {
    opacity: 0.35;
    transform: scale(0.7);
  }

  70%,
  100% {
    opacity: 0;
    transform: scale(1.9);
  }
}

/* The console-wide fallback in base.css already collapses animation durations; this
   rule also removes the ring so no invisible layer is left animating on a paint. */
@media (prefers-reduced-motion: reduce) {
  .status-tag__dot--pulse::after {
    animation: none;
    opacity: 0;
  }
}
</style>
