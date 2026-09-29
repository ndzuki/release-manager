<script setup lang="ts">
import { t } from '@/i18n/messages';

interface ForbiddenStateProps {
  title?: string;
  message?: string;
  actionLabel?: string;
}

withDefaults(defineProps<ForbiddenStateProps>(), {
  title: t('state.accessDenied'),
  message: t('state.accessDeniedMessage'),
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
  color: var(--color-subtle);
  font-size: var(--font-size-3xl);
  font-weight: 800;
}

.forbidden-state__title,
.forbidden-state__text {
  margin: 0;
}

.forbidden-state__title {
  font-size: var(--font-size-lg);
}

.forbidden-state__text {
  max-width: 32rem;
  color: var(--color-muted);
}

.forbidden-state__action {
  margin-top: 0.5rem;
  padding: 0.45rem 0.75rem;
  border: 1px solid var(--color-border-strong);
  border-radius: 0.375rem;
  background: var(--color-surface);
  cursor: pointer;
}
</style>
