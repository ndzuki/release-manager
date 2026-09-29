import { nextTick, onBeforeUnmount, onMounted, watch, type Ref } from 'vue';

/**
 * Focus management for overlays (ADR-029 clause 4).
 *
 * Baseline before AppDialog (measured, docs/ux-review.md): **no** dialog trapped
 * Tab — the app had zero `keydown` handlers covering a modal — so a keyboard user
 * could tab into the page rendered *behind* the backdrop. Focus was handled ad
 * hoc: CancelOperationDialog focused its first field on open and
 * OperationDetailPage tracked a `lastFocused` element to restore. The remaining
 * six dialogs did neither. The app also had 0 `<Teleport>` and 2 `:focus-visible`
 * rules.
 *
 * Escape is handled here at the document level (so it works wherever focus sits,
 * as WAI-ARIA APG expects) but *owned* by the caller: "close" is a product
 * decision, so the dialog passes `onEscape` and emits `close` itself.
 */
const FOCUSABLE_SELECTOR = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(', ');

export interface FocusTrapOptions {
  /** Whether the overlay is currently open. */
  active: Ref<boolean>;
  /**
   * Called on Escape while this overlay is the topmost open one. Omit to leave
   * Escape unhandled.
   */
  onEscape?: () => void;
}

/*
 * Open overlays, oldest first. Only the LAST entry reacts to Tab/Escape: without
 * this, two simultaneous dialogs each register a document-level capture listener
 * and the lower one can preventDefault Tab and pull focus behind the top overlay
 * (found by the independent review of TASK-177).
 */
interface TrapEntry {
  token: object;
  container: Ref<HTMLElement | null>;
}

const trapStack: TrapEntry[] = [];

export interface FocusTrap {
  /** Focus the first focusable element (or the container) on demand. */
  focusFirst: () => void;
}

export function useFocusTrap(container: Ref<HTMLElement | null>, options: FocusTrapOptions): FocusTrap {
  let previouslyFocused: HTMLElement | null = null;
  let listening = false;
  // Identity is a per-instance token, never the DOM ref: the ref is null before
  // the panel renders and again after it unmounts, which would make `indexOf`
  // miss and leave a dirty stack entry that disables every later overlay.
  const token = {};

  function isTopmost(): boolean {
    return trapStack[trapStack.length - 1]?.token === token;
  }

  function focusableElements(): HTMLElement[] {
    const root = container.value;
    if (!root) return [];
    // No layout-based filtering (offsetParent/visibility): this runs in a DOM
    // implementation without layout, and a hidden-but-present control is still a
    // better focus target than none.
    return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR));
  }

  function focusFirst(): void {
    const [first] = focusableElements();
    (first ?? container.value)?.focus();
  }

  function onKeydown(event: KeyboardEvent): void {
    if (!isTopmost()) return;
    if (event.key === 'Escape') {
      options.onEscape?.();
      return;
    }
    if (event.key !== 'Tab') return;
    const focusables = focusableElements();
    if (focusables.length === 0) {
      // Nothing focusable inside: keep focus on the panel instead of letting the
      // browser move it to the document behind the overlay.
      event.preventDefault();
      container.value?.focus();
      return;
    }
    const first = focusables[0]!;
    const last = focusables[focusables.length - 1]!;
    const active = document.activeElement;
    if (event.shiftKey && (active === first || active === container.value)) {
      event.preventDefault();
      last.focus();
      return;
    }
    if (!event.shiftKey && active === last) {
      event.preventDefault();
      first.focus();
    }
  }

  function start(): void {
    if (listening) return;
    listening = true;
    document.addEventListener('keydown', onKeydown, true);
  }

  // The panel lives inside `v-if="open"`, so the stack entry is pushed once it
  // exists: from onMounted for the first mount, from after the render tick when
  // `open` flips (the watcher runs pre-flush).
  function registerTopmost(): void {
    if (trapStack.some((entry) => entry.token === token)) return;
    trapStack.push({ token, container });
  }

  function stop(): void {
    if (!listening) return;
    listening = false;
    document.removeEventListener('keydown', onKeydown, true);
    const index = trapStack.findIndex((entry) => entry.token === token);
    if (index !== -1) trapStack.splice(index, 1);
  }

  watch(
    options.active,
    async (isActive) => {
      if (isActive) {
        previouslyFocused = (document.activeElement as HTMLElement | null) ?? null;
        // Register before the await: otherwise there is a full tick in which Tab
        // still escapes the overlay.
        // Attach the listener now and register the stack entry after the tick.
        // Only attaching is safe pre-render: isTopmost() is still false until the
        // entry exists, so a keydown in that window is ignored rather than
        // mis-routed.
        start();
        // After the tick the panel exists — true both for the first mount and for
        // a dialog that is mounted closed and opened later (the pattern
        // CustomerDetailPage uses for DisableCustomerDialog).
        await nextTick();
        registerTopmost();
        focusFirst();
        return;
      }
      stop();
      // Return focus where the user was: closing a dialog must not dump them at
      // the top of the page.
      if (previouslyFocused && typeof previouslyFocused.focus === 'function') {
        previouslyFocused.focus();
      }
      previouslyFocused = null;
    },
    { immediate: true },
  );

  // First mount: onMounted runs inside mount(), so the overlay is on the stack
  // before any key can reach it.
  onMounted(() => {
    if (options.active.value) registerTopmost();
  });

  onBeforeUnmount(() => {
    stop();
    if (previouslyFocused && typeof previouslyFocused.focus === 'function') {
      previouslyFocused.focus();
    }
    previouslyFocused = null;
  });

  return { focusFirst };
}
