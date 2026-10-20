<script setup lang="ts">
import { computed } from 'vue';

/*
 * Inline notice with an optional actions slot.
 *
 * Two decisions worth stating: the live region follows the tone (an error is `alert` so
 * it interrupts, the other three are `status` so they wait for a pause), and the surface
 * is a 10% color-mix of the palette BASE tone (`--er`, `--wn`, …) over the page
 * background instead of a second palette of pastel backgrounds. The label takes the
 * readable `--color-*-ink` / `-strong` step instead of that base: a base-coloured label
 * on its own tint measures as low as 3.93:1 in the light theme (TASK-394).
 */
type AlertTone = 'error' | 'warning' | 'info' | 'success';

const props = defineProps<{
  tone: AlertTone;
  title: string;
  message?: string;
}>();

const role = computed(() => (props.tone === 'error' || props.tone === 'warning' ? 'alert' : 'status'));
</script>

<template>
  <div class="alert" :class="`alert--${tone}`" :role="role">
    <span class="alert__mark" aria-hidden="true" />
    <div class="alert__body">
      <p class="alert__title">{{ title }}</p>
      <p v-if="message" class="alert__message">{{ message }}</p>
    </div>
    <div v-if="$slots.actions" class="alert__actions">
      <slot name="actions" />
    </div>
  </div>
</template>

<style scoped>
.alert {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  padding: 10px 12px;
  border: 1px solid color-mix(in srgb, var(--mu) 35%, var(--bd));
  border-radius: var(--r);
  background: color-mix(in srgb, var(--mu) 10%, var(--bg));
}

/* Each tone states the chrome and the label together: the surface, border and mark mix
   from the palette BASE colour, only the label takes the ink step. The title is
   re-stated in the text colour so it stays neutral. */
.alert--error {
  border-color: color-mix(in srgb, var(--color-error) 35%, var(--bd));
  background-color: color-mix(in srgb, var(--color-error) 10%, var(--bg));
  color: var(--color-error-strong);
}

.alert--warning {
  border-color: color-mix(in srgb, var(--color-warning) 35%, var(--bd));
  background-color: color-mix(in srgb, var(--color-warning) 10%, var(--bg));
  color: var(--color-warning-ink);
}

.alert--info {
  border-color: color-mix(in srgb, var(--color-info) 35%, var(--bd));
  background-color: color-mix(in srgb, var(--color-info) 10%, var(--bg));
  color: var(--color-info-ink);
}

.alert--success {
  border-color: color-mix(in srgb, var(--color-success) 35%, var(--bd));
  background-color: color-mix(in srgb, var(--color-success) 10%, var(--bg));
  color: var(--color-success-ink);
}

.alert__mark {
  flex: none;
  width: 3px;
  align-self: stretch;
  min-height: 18px;
  border-radius: 999px;
  background: var(--mu);
  margin-top: 2px;
}

.alert--error .alert__mark {
  background: var(--color-error);
}

.alert--warning .alert__mark {
  background: var(--color-warning);
}

.alert--info .alert__mark {
  background: var(--color-info);
}

.alert--success .alert__mark {
  background: var(--color-success);
}

.alert__body {
  display: grid;
  flex: 1;
  gap: 2px;
  min-width: 0;
}

.alert__title {
  margin: 0;
  color: var(--tx);
  font-size: var(--font-size-md);
  font-weight: var(--font-weight-medium);
}

.alert__message {
  margin: 0;
  color: color-mix(in srgb, var(--tx) 78%, var(--mu));
  font-size: var(--font-size-sm);
  overflow-wrap: anywhere;
}

.alert__actions {
  display: flex;
  flex: none;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}
</style>
