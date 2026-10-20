<script setup lang="ts">
/*
 * Vertical timeline: a dot per item on a continuous rail, newest first is the caller's
 * choice. `now` marks the item that is currently happening, so it gets the accent dot
 * plus a pulse; the rail is drawn between dots with a pseudo element (the last item has
 * none) rather than with a border on the list, which would overshoot the final dot.
 */
defineProps<{
  items: Array<{ title: string; meta?: string; now?: boolean }>;
}>();
</script>

<template>
  <ol class="timeline">
    <li v-for="(item, index) in items" :key="`${index}-${item.title}`" class="timeline__item">
      <span class="timeline__rail" aria-hidden="true">
        <span class="timeline__dot" :class="{ 'timeline__dot--now': item.now }" />
      </span>
      <div class="timeline__body">
        <p class="timeline__title">{{ item.title }}</p>
        <p v-if="item.meta" class="timeline__meta">{{ item.meta }}</p>
      </div>
    </li>
  </ol>
</template>

<style scoped>
.timeline {
  display: grid;
  margin: 0;
  padding: 0;
  list-style: none;
}

.timeline__item {
  display: grid;
  grid-template-columns: 16px minmax(0, 1fr);
  gap: 10px;
  padding-bottom: 14px;
}

.timeline__item:last-child {
  padding-bottom: 0;
}

/* The rail is the column itself: a full-height line under the dot, omitted after the
   last item so the timeline does not appear to continue. */
.timeline__rail {
  position: relative;
  display: flex;
  justify-content: center;
}

.timeline__rail::after {
  position: absolute;
  top: 12px;
  bottom: -2px;
  left: 50%;
  width: 1px;
  background: var(--bd);
  content: '';
}

.timeline__item:last-child .timeline__rail::after {
  display: none;
}

.timeline__dot {
  position: relative;
  z-index: 1;
  width: 7px;
  height: 7px;
  margin-top: 6px;
  border-radius: 50%;
  background: var(--mu);
}

.timeline__dot--now {
  background: var(--ac);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--ac) 22%, transparent);
  animation: timeline-now 1.8s ease-in-out infinite;
}

.timeline__body {
  display: grid;
  gap: 2px;
  min-width: 0;
  padding-bottom: 2px;
}

.timeline__title {
  margin: 0;
  color: var(--tx);
  font-size: var(--font-size-md);
  overflow-wrap: anywhere;
}

.timeline__meta {
  margin: 0;
  color: var(--mu);
  font-size: var(--font-size-xs);
}

@keyframes timeline-now {
  0%,
  100% {
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--ac) 22%, transparent);
  }

  50% {
    box-shadow: 0 0 0 6px color-mix(in srgb, var(--ac) 8%, transparent);
  }
}

@media (prefers-reduced-motion: reduce) {
  .timeline__dot--now {
    animation: none;
  }
}
</style>
