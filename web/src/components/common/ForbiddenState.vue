<script setup lang="ts">
interface ForbiddenStateProps {
  title?: string;
  message?: string;
  actionLabel?: string;
}

withDefaults(defineProps<ForbiddenStateProps>(), {
  title: 'Access denied',
  message: 'You do not have permission to access this resource.',
  actionLabel: '',
});

const emit = defineEmits<{ action: [] }>();
</script>

<template>
  <section class="forbidden-state" role="alert" aria-labelledby="forbidden-state-title">
    <span class="forbidden-state__code" aria-hidden="true">403</span>
    <h2 id="forbidden-state-title" class="forbidden-state__title">{{ title }}</h2>
    <p class="forbidden-state__text">{{ message }}</p>
    <slot name="action">
      <button v-if="actionLabel" class="forbidden-state__action" type="button" @click="emit('action')">
        {{ actionLabel }}
      </button>
    </slot>
  </section>
</template>

<style scoped>
.forbidden-state {
  display: grid;
  min-height: 18rem;
  place-items: center;
  align-content: center;
  gap: 0.6rem;
  padding: 3rem 1rem;
  text-align: center;
}

.forbidden-state__code {
  color: var(--color-muted, #94a3b8);
  font-size: 3rem;
  font-weight: 800;
}

.forbidden-state__title,
.forbidden-state__text {
  margin: 0;
}

.forbidden-state__title {
  font-size: 1.2rem;
}

.forbidden-state__text {
  max-width: 32rem;
  color: var(--color-muted, #64748b);
}

.forbidden-state__action {
  margin-top: 0.5rem;
  padding: 0.45rem 0.75rem;
  border: 1px solid var(--color-border, #cbd5e1);
  border-radius: 0.375rem;
  background: var(--color-surface, #fff);
  cursor: pointer;
}
</style>
