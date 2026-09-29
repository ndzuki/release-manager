<script setup lang="ts">
import { computed, useId, useTemplateRef } from 'vue';
import { useFocusTrap } from '@/composables/useFocusTrap';

/*
 * The one dialog primitive (ADR-029 clause 3).
 *
 * The console had eight hand-rolled dialogs with eight different behaviours:
 * `<Teleport>` was used zero times, none trapped Tab, focus return was
 * implemented ad hoc (one page), and four rendered `role="dialog"` without
 * `aria-describedby`. This is the primitive those overlays migrate to; two
 * destructive dialogs use it today and **six are still hand-rolled**
 * (TASK-177 AC-177-06). Until they migrate, the accessible behaviour is defined
 * here rather than everywhere.
 *
 * Contract: props down / events up. The dialog never closes itself — it emits
 * `close` and the owner decides (a destructive confirm must stay open while its
 * request is in flight).
 */
interface Props {
  open: boolean;
  title: string;
  description?: string;
  /** `alertdialog` for irreversible actions (focus is announced as urgent). */
  danger?: boolean;
  /** Backdrop click closes by default; destructive flows pass `false`. */
  closeOnBackdrop?: boolean;
  /** Escape closes by default; a pending submission passes `false`. */
  closeOnEscape?: boolean;
}

const props = withDefaults(defineProps<Props>(), {
  description: undefined,
  danger: false,
  closeOnBackdrop: true,
  closeOnEscape: true,
});

const emit = defineEmits<{
  close: [];
}>();

const titleId = useId();
const descriptionId = useId();
const panel = useTemplateRef<HTMLDivElement>('panel');

useFocusTrap(panel, {
  active: computed(() => props.open),
  // Document-level (not bound to the backdrop) so Escape works wherever focus
  // sits — WAI-ARIA APG expects Escape to close the dialog regardless, and an
  // async re-render can move focus out of the panel.
  onEscape: () => {
    if (props.closeOnEscape) requestClose();
  },
});

function requestClose(): void {
  emit('close');
}

function onBackdropClick(): void {
  if (props.closeOnBackdrop) requestClose();
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="app-dialog" @click.self="onBackdropClick">
      <div
        ref="panel"
        class="app-dialog__panel"
        :class="{ 'app-dialog__panel--danger': danger }"
        :role="danger ? 'alertdialog' : 'dialog'"
        aria-modal="true"
        :aria-labelledby="titleId"
        :aria-describedby="description ? descriptionId : undefined"
        tabindex="-1"
      >
        <header class="app-dialog__header">
          <h2 :id="titleId" class="app-dialog__title">{{ title }}</h2>
        </header>
        <p v-if="description" :id="descriptionId" class="app-dialog__description">{{ description }}</p>
        <div class="app-dialog__body">
          <slot />
        </div>
        <footer class="app-dialog__footer">
          <slot name="footer" />
        </footer>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.app-dialog {
  position: fixed;
  inset: 0;
  z-index: var(--z-dialog-backdrop);
  display: grid;
  place-items: center;
  padding: var(--space-4);
  background: rgb(15 23 42 / 60%);
}

.app-dialog__panel {
  /* The backdrop owns the stacking context (--z-dialog-backdrop); the panel pins
     itself one layer above it explicitly rather than relying on DOM order. */
  z-index: var(--z-dialog);
  display: grid;
  gap: var(--space-3);
  width: min(34rem, 100%);
  max-height: calc(100vh - 2 * var(--space-6));
  overflow: auto;
  padding: var(--space-5);
  border-radius: var(--radius-xl);
  background: var(--color-surface);
  box-shadow: var(--shadow-lg);
}

.app-dialog__panel--danger {
  border-top: 3px solid var(--color-error);
}

.app-dialog__title {
  margin: 0;
  font-size: var(--font-size-lg);
  line-height: var(--line-height-tight);
}

.app-dialog__description {
  margin: 0;
  color: var(--color-muted);
}

.app-dialog__body {
  display: grid;
  gap: var(--space-3);
}

.app-dialog__footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-2);
}

.app-dialog__footer:empty {
  display: none;
}

/* Footer buttons are slotted, so they need a baseline from here: the dialogs
   that migrated dropped their own `button {…}` blocks, which would otherwise
   leave Cancel/Confirm at UA defaults (found by the independent review). Tone
   (danger colours) stays the caller's scoped rule. */
/* Two constraints, and both matter:
   1. `:slotted()` must NOT sit inside `:where()` — the scoped compiler leaves it
      untouched there, and `:where()` is a forgiving list, so an unknown pseudo-
      class silently turns the whole selector into a no-match (this rule was dead
      CSS once; caught by the independent review of TASK-179).
   2. the baseline must lose to a caller's variant (.primary/.danger), so the
      leading compound is wrapped in `:where()`: the compiled selector
      `:where(.app-dialog__footer) button[data-v-x-s]` scores (0,1,1) against the
      caller's (0,2,0). */
:where(.app-dialog__footer) :slotted(button) {
  padding: var(--space-2) var(--space-4);
  border: 1px solid var(--color-border-strong);
  border-radius: var(--radius-md);
  background: var(--color-surface);
  color: var(--color-text);
  font: inherit;
  font-weight: var(--font-weight-medium);
  cursor: pointer;
}

:where(.app-dialog__footer) :slotted(button:disabled) {
  cursor: not-allowed;
  opacity: 0.55;
}
</style>
