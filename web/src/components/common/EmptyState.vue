<script setup lang="ts">
interface EmptyStateProps {
  title?: string;
  message?: string;
  actionLabel?: string;
}

withDefaults(defineProps<EmptyStateProps>(), {
  title: 'No data',
  message: '',
  actionLabel: '',
});

const emit = defineEmits<{ action: [] }>();
</script>

<template>
  <section class="empty-state" aria-labelledby="empty-state-title">
    <span class="empty-state__icon" aria-hidden="true">∅</span>
    <h2 id="empty-state-title" class="empty-state__title">{{ title }}</h2>
    <p v-if="message" class="empty-state__text">{{ message }}</p>
    <slot name="action">
      <button v-if="actionLabel" class="empty-state__action" type="button" @click="emit('action')">
        {{ actionLabel }}
      </button>
    </slot>
  </section>
</template>

<style scoped>
.empty-state {
  display: grid;
  min-height: 12rem;
  place-items: center;
  align-content: center;
  gap: 0.6rem;
  padding: 3rem 1rem;
  text-align: center;
}

.empty-state__icon {
  font-size: 2rem;
  color: var(--color-muted, #94a3b8);
}

.empty-state__title,
.empty-state__text {
  margin: 0;
}

.empty-state__title {
  font-size: 1rem;
}

.empty-state__text {
  color: var(--color-muted, #64748b);
  font-size: 0.875rem;
}

.empty-state__action {
  margin-top: 0.5rem;
  padding: 0.45rem 0.75rem;
  border: 1px solid var(--color-border, #cbd5e1);
  border-radius: 0.375rem;
  background: var(--color-surface, #fff);
  cursor: pointer;
}
</style>
