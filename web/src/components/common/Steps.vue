<script setup lang="ts">
/*
 * Progress steps: 24px dots joined by a rule, the current step carrying a breathing ring.
 * `state` is constrained to three values on purpose — the component does not infer
 * "current" from an index prop, so a caller whose flow skips or re-enters steps does not
 * have to bend the model.
 *
 * Accessibility: an ordered list (the order is the flow), `aria-current="step"` on the
 * current item so a screen-reader user can locate themselves without counting, and the
 * dot/check decorations are hidden because the label already says the step.
 */
type StepState = 'done' | 'now' | 'todo';

defineProps<{
  steps: Array<{ label: string; state: StepState }>;
}>();
</script>

<template>
  <ol class="steps">
    <li
      v-for="step in steps"
      :key="step.label"
      class="steps__item"
      :class="`steps__item--${step.state}`"
      :aria-current="step.state === 'now' ? 'step' : undefined"
    >
      <span class="steps__dot" aria-hidden="true" />
      <span class="steps__label">{{ step.label }}</span>
    </li>
  </ol>
</template>

<style scoped>
.steps {
  display: flex;
  flex-wrap: wrap;
  gap: 0;
  margin: 0;
  padding: 0;
  list-style: none;
}

.steps__item {
  display: flex;
  flex: 1 1 auto;
  gap: 8px;
  align-items: center;
  min-width: 0;
  color: var(--mu);
  font-size: var(--font-size-xs);
}

/* Connector between dots, drawn on the item rather than as a separate element so a
   wrapped row does not leave a dangling line. */
.steps__item:not(:last-child)::after {
  flex: 1;
  height: 1px;
  margin: 0 10px;
  background: var(--bd);
  content: '';
}

.steps__dot {
  position: relative;
  display: grid;
  flex: none;
  place-items: center;
  width: 24px;
  height: 24px;
  border: 1px solid var(--bd);
  border-radius: 50%;
  background: var(--sf);
  color: var(--mu);
  font-size: var(--font-size-xs);
}

.steps__item--done .steps__dot {
  border-color: color-mix(in srgb, var(--ok) 45%, var(--bd));
  background: color-mix(in srgb, var(--ok) 14%, var(--bg));
  color: var(--color-success-ink);
}

.steps__item--done .steps__dot::before {
  content: '✓';
}

.steps__item--now .steps__dot {
  border-color: var(--pr);
  background: var(--pr);
  color: var(--on);
}

/* The pulse is a ring, not the dot itself: animating the dot would change the layout
   box and make the row twitch. */
.steps__item--now .steps__dot::after {
  position: absolute;
  inset: -4px;
  border: 1px solid var(--pr);
  border-radius: 50%;
  animation: steps-pulse 1.8s ease-out infinite;
  content: '';
}

.steps__item--now .steps__label {
  color: var(--tx);
  font-weight: var(--font-weight-medium);
}

.steps__label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@keyframes steps-pulse {
  0% {
    opacity: 0.55;
    transform: scale(0.85);
  }

  70%,
  100% {
    opacity: 0;
    transform: scale(1.25);
  }
}

@media (prefers-reduced-motion: reduce) {
  .steps__item--now .steps__dot::after {
    animation: none;
    opacity: 0;
  }
}
</style>
