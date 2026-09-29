<script setup lang="ts">
import { t } from '@/i18n/messages';

interface ErrorStateProps {
  title?: string;
  message?: string;
  details?: string;
  actionLabel?: string;
}

withDefaults(defineProps<ErrorStateProps>(), {
  title: t('error.title'),
  message: '',
  details: '',
  actionLabel: '',
});

const emit = defineEmits<{ action: [] }>();
</script>

<template>
  <section class="error-state" role="alert" aria-labelledby="error-state-title">
    <span class="error-state__icon" aria-hidden="true">!</span>
    <h2 id="error-state-title" class="error-state__title">{{ title }}</h2>
    <p v-if="message" class="error-state__text">{{ message }}</p>
    <details v-if="details" class="error-state__details">
      <summary>{{ t('error.technicalDetails') }}</summary>
      <pre>{{ details }}</pre>
    </details>
    <slot name="action">
      <button v-if="actionLabel" class="error-state__action" type="button" @click="emit('action')">
        {{ actionLabel }}
      </button>
    </slot>
  </section>
</template>

<style scoped>
.error-state {
  display: grid;
  min-height: 8rem;
  place-items: center;
  align-content: center;
  gap: 0.6rem;
  padding: 1.5rem;
  border-radius: 0.5rem;
  background: var(--color-danger-soft);
  text-align: center;
}

.error-state__icon {
  display: grid;
  width: 2rem;
  height: 2rem;
  place-items: center;
  border-radius: 50%;
  background: var(--color-danger);
  color: var(--color-on-accent);
  font-weight: 800;
}

.error-state__title,
.error-state__text {
  margin: 0;
}

.error-state__title {
  color: var(--color-error-strong);
  font-size: var(--font-size-base);
}

.error-state__text,
.error-state__details {
  color: var(--color-danger-ink-deep);
  font-size: var(--font-size-md);
}

.error-state__details pre {
  max-width: 32rem;
  white-space: pre-wrap;
  text-align: left;
}

.error-state__action {
  padding: 0.45rem 0.75rem;
  border: 1px solid var(--color-danger-border);
  border-radius: 0.375rem;
  background: var(--color-surface);
  color: var(--color-error-strong);
  cursor: pointer;
}
</style>
