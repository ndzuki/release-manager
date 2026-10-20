<script setup lang="ts">
import { computed } from 'vue';

/*
 * Inline notice with an optional actions slot.
 *
 * Two decisions worth stating: the live region follows the tone (an error is `alert` so
 * it interrupts, the other three are `status` so they wait for a pause), and the surface
 * is a 10% color-mix of the tone over the page background instead of a second palette of
 * pastel backgrounds. That is what makes the same component legible in both themes.
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
  border: 1px solid color-mix(in srgb, currentColor 35%, var(--bd));
  border-radius: var(--r);
  background: color-mix(in srgb, currentColor 10%, var(--bg));
}

/* The tone is carried by `color`; the surface, border and mark mix from currentColor.
   The title is re-stated in the text colour so only the chrome takes the tone. */
.alert--error {
  color: var(--er);
}

.alert--warning {
  color: var(--wn);
}

.alert--info {
  /* Info has its own hue (cyan); --ac equals --ok, so the neutral tone read as success. */
  color: var(--color-info);
}

.alert--success {
  color: var(--ok);
}

.alert__mark {
  flex: none;
  width: 3px;
  align-self: stretch;
  min-height: 18px;
  border-radius: 999px;
  background: currentColor;
  margin-top: 2px;
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
