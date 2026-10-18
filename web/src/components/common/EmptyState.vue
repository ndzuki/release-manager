<script setup lang="ts">
import { useId } from 'vue';
import { t } from '@/i18n/messages';

interface EmptyStateProps {
  title?: string;
  message?: string;
  actionLabel?: string;
}

withDefaults(defineProps<EmptyStateProps>(), {
  title: t('state.noData'),
  message: '',
  actionLabel: '',
});

const emit = defineEmits<{ action: [] }>();

/**
 * UX-037 (W14): a fixed DOM id was shared by every instance, so two states on
 * one page produced duplicate ids and the second one's aria-labelledby pointed
 * at the first one's heading (or at nothing). useId() gives each instance its
 * own pair.
 */
const titleId = useId();
</script>

<template>
  <section class="empty-state" :aria-labelledby="titleId">
    <span class="empty-state__icon" aria-hidden="true">∅</span>
    <h2 :id="titleId" class="empty-state__title">{{ title }}</h2>
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
  font-size: var(--font-size-2xl);
  color: var(--color-subtle);
}

.empty-state__title,
.empty-state__text {
  margin: 0;
}

.empty-state__title {
  font-size: var(--font-size-base);
}

.empty-state__text {
  color: var(--color-muted);
  font-size: var(--font-size-md);
}

.empty-state__action {
  margin-top: 0.5rem;
  padding: 0.45rem 0.75rem;
  border: 1px solid var(--color-border-strong);
  border-radius: 0.375rem;
  background: var(--color-surface);
  cursor: pointer;
}
</style>
