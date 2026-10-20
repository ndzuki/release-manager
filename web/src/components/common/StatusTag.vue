<script setup lang="ts">
/*
 * Direction C status pill: a 6px dot plus a label on a 10% tint of its own tone.
 *
 * The tone is split into two roles on purpose (TASK-394, semantic-ink migration): the
 * tint, border and dot mix from the palette BASE colour (`--color-success` etc., which
 * resolve to `--ok`/`--wn`/`--er`/`--k`) while the label uses the readable
 * `--color-*-ink` / `-strong` step. Deriving the tint from the text colour would make
 * the pill lighter exactly when the ink (dark in light theme) is mixed toward `--tx`,
 * and a base-coloured label on its own 10% tint measures 4.24:1 in the light theme —
 * below AA.
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
  border: 1px solid color-mix(in srgb, var(--mu) 28%, transparent);
  border-radius: 999px;
  background: color-mix(in srgb, var(--mu) 10%, transparent);
  font-size: var(--font-size-xs);
  line-height: 1.7;
  white-space: nowrap;
}

/* Each tone states the pair in one place: the chrome (border, tint, dot) mixes from the
   palette BASE colour, the label takes its ink step. No component-local custom property
   is used for the tone — tokens.test.ts requires every `var()` reference to be declared
   in tokens.css. */
.status-tag--ok {
  border-color: color-mix(in srgb, var(--color-success) 28%, transparent);
  background: color-mix(in srgb, var(--color-success) 10%, transparent);
  color: var(--color-success-ink);
}

.status-tag--wn {
  border-color: color-mix(in srgb, var(--color-warning) 28%, transparent);
  background: color-mix(in srgb, var(--color-warning) 10%, transparent);
  color: var(--color-warning-ink);
}

.status-tag--er {
  border-color: color-mix(in srgb, var(--color-error) 28%, transparent);
  background: color-mix(in srgb, var(--color-error) 10%, transparent);
  color: var(--color-error-strong);
}

.status-tag--info {
  border-color: color-mix(in srgb, var(--color-info) 28%, transparent);
  background: color-mix(in srgb, var(--color-info) 10%, transparent);
  color: var(--color-info-ink);
}

/* Neutral keeps the base chrome, so it only needs its label. */
.status-tag--neutral {
  color: var(--mu);
}

.status-tag__dot {
  position: relative;
  flex: none;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--mu);
}

.status-tag__dot--pulse::after {
  position: absolute;
  inset: -3px;
  border-radius: 50%;
  background: var(--mu);
  opacity: 0.35;
  animation: status-tag-pulse 1.8s ease-out infinite;
  content: '';
}

.status-tag--ok .status-tag__dot,
.status-tag--ok .status-tag__dot--pulse::after {
  background: var(--color-success);
}

.status-tag--wn .status-tag__dot,
.status-tag--wn .status-tag__dot--pulse::after {
  background: var(--color-warning);
}

.status-tag--er .status-tag__dot,
.status-tag--er .status-tag__dot--pulse::after {
  background: var(--color-error);
}

.status-tag--info .status-tag__dot,
.status-tag--info .status-tag__dot--pulse::after {
  background: var(--color-info);
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
