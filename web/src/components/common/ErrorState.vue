<script setup lang="ts">
interface ErrorStateProps {
  title?: string;
  message?: string;
  details?: string;
  actionLabel?: string;
}

withDefaults(defineProps<ErrorStateProps>(), {
  title: 'Something went wrong',
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
      <summary>Technical details</summary>
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
  background: #fef2f2;
  text-align: center;
}

.error-state__icon {
  display: grid;
  width: 2rem;
  height: 2rem;
  place-items: center;
  border-radius: 50%;
  background: #dc2626;
  color: #fff;
  font-weight: 800;
}

.error-state__title,
.error-state__text {
  margin: 0;
}

.error-state__title {
  color: #991b1b;
  font-size: 1rem;
}

.error-state__text,
.error-state__details {
  color: #7f1d1d;
  font-size: 0.875rem;
}

.error-state__details pre {
  max-width: 32rem;
  white-space: pre-wrap;
  text-align: left;
}

.error-state__action {
  padding: 0.45rem 0.75rem;
  border: 1px solid #ef4444;
  border-radius: 0.375rem;
  background: #fff;
  color: #991b1b;
  cursor: pointer;
}
</style>
