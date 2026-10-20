<script setup lang="ts">
/*
 * A single number with a label and an optional hint. Direction C renders the value at
 * 28px/650 with tabular figures, so a row of cards keeps its digits aligned; the tone
 * only colours the number (the label and hint stay neutral so the card does not become a
 * wall of colour).
 */
type StatTone = 'neutral' | 'ok' | 'wn' | 'er' | 'info';

withDefaults(
  defineProps<{
    label: string;
    value: string | number;
    /** Secondary context (unit, window, "since last sync"). */
    hint?: string;
    tone?: StatTone;
  }>(),
  { hint: undefined, tone: 'neutral' },
);
</script>

<template>
  <div class="stat-card" :class="`stat-card--${tone}`">
    <p class="stat-card__label">{{ label }}</p>
    <p class="stat-card__value">{{ value }}</p>
    <p v-if="hint" class="stat-card__hint">{{ hint }}</p>
  </div>
</template>

<style scoped>
.stat-card {
  display: grid;
  gap: 4px;
  min-width: 0;
  padding: 14px 16px;
  border: 1px solid var(--bd);
  border-radius: var(--r);
  background: var(--sf);
}

.stat-card__label {
  margin: 0;
  color: var(--mu);
  font-size: var(--font-size-xs);
}

.stat-card__value {
  margin: 0;
  font-size: var(--font-size-stat);
  font-variant-numeric: tabular-nums;
  font-weight: var(--font-weight-heading);
  letter-spacing: -0.02em;
  line-height: 1.15;
}

/* Only the number is toned; the label and hint stay neutral so a row of cards is
   readable instead of a wall of colour. */
.stat-card--neutral .stat-card__value {
  color: var(--tx);
}

.stat-card--ok .stat-card__value {
  color: var(--ok);
}

.stat-card--wn .stat-card__value {
  color: var(--wn);
}

.stat-card--er .stat-card__value {
  color: var(--er);
}

.stat-card--info .stat-card__value {
  color: var(--ac);
}

.stat-card__hint {
  margin: 0;
  color: var(--mu);
  font-size: var(--font-size-xs);
}
</style>
