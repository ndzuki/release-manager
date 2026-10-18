import {
  computed,
  onBeforeUnmount,
  ref,
  shallowRef,
  toValue,
  watch,
  type MaybeRefOrGetter,
  type Ref,
} from 'vue';

/*
 * W15 (TASK-325): the operation timeline, the customer history and the convergence
 * task list are loaded whole because their RPCs carry no cursor (see
 * docs/delivery-evidence.md §5.2 W15 for the field-level evidence). Server-side
 * pagination therefore cannot be added from the console alone.
 *
 * What the console CAN bound is the render cost: a 500-entry timeline (the store's
 * MAX_TIMELINE_ENTRIES cap) is 500 rows of layout and paint on every frame, and an
 * unattended customer accumulates history without limit. This composable keeps only
 * the rows intersecting the scroll viewport in the DOM.
 *
 * Deliberate limits, so nobody mistakes it for the missing pagination:
 *  - the payload is still loaded whole; this fixes rendering, not transfer size;
 *  - a list of `threshold` rows or fewer is not windowed at all, so it behaves and
 *    lays out as before: no nested scrollbar, no extra tab stop. It is *not* a
 *    byte-identical DOM, though: the scroll wrapper is always in the tree (the
 *    spacers are padding on the `<ol>`, and padding defeats a `max-height` on the
 *    padded list itself), so a short list carries one extra unstyled `<div>`;
 *  - the threshold is inclusive: 60 rows are all in the DOM, 61 is the first
 *    windowed count (`virtualized` is `length > threshold`);
 *  - the list keeps its `<ol>`/`<li>` structure and the rows keep their real heights
 *    (measured, not assumed), so list semantics and screen-reader positions survive.
 *
 * Accessibility cost, stated rather than left implicit: off-window rows are not
 * rendered, so anything focusable inside one leaves the tab order and the
 * accessibility tree until its row scrolls in. In the timeline that is only the
 * ERROR row's copy-Operation-ID / copy-Request-ID buttons (`TimelineEntryItem.vue`);
 * a customer-history row holds no control, and W14 already removed the per-row tab
 * stop from the timeline `<li>`, so this is not the 500-stops-per-page case it
 * replaced. Mitigation: past the threshold the wrapper becomes a named, focusable
 * region (`role="region"` + `tabindex="0"`), so a keyboard user focuses it once and
 * arrow-scrolls the row they need into the window. WCAG 2.1.1 is met through that
 * route, not by every row being tabbable at all times.
 *
 * That is a different call from the convergence task list, which is deliberately not
 * virtualized: its checkboxes and Continue buttons sit on *every* row, and the list is
 * meant to be traversed row by row by keyboard, so windowing would remove the primary
 * interaction from most rows. The difference is one of degree, not principle — here
 * only ERROR rows carry controls, there every row does — and the task list still needs
 * the server cursor.
 */

/** Row height assumed until ResizeObserver reports the real one. */
export const VIRTUAL_LIST_ESTIMATE_PX = 64;
/** Below this many rows the list renders as-is; the window is only a capacity tool. */
export const VIRTUAL_LIST_THRESHOLD = 60;
/** Rows kept rendered beyond each viewport edge so a fast scroll does not flash blanks. */
export const VIRTUAL_LIST_OVERSCAN = 6;

export interface VirtualListOptions<T> {
  /** Stable identity per row. Measurements are cached by it, so prepends do not shift them. */
  keyOf: (item: T) => string;
  estimate?: number;
  threshold?: number;
  overscan?: number;
  /**
   * Vertical spacing the list adds between rows (the CSS `gap`). Measured heights are
   * the row box only, so the gap has to be added here or every offset drifts by one gap.
   */
  gap?: number;
  /**
   * Viewport height used until the container can be measured. happy-dom reports 0 for
   * clientHeight, so unit tests pin the window through this value.
   */
  viewportFallback?: number;
}

export interface VirtualWindow {
  /** First rendered row index (inclusive). */
  start: number;
  /** One past the last rendered row index. */
  end: number;
}

/** Prefix sums of the row heights; length is `keys.length + 1`. */
export function buildOffsets(
  keys: readonly string[],
  measured: ReadonlyMap<string, number>,
  estimate: number,
  gap = 0,
): number[] {
  const offsets = new Array<number>(keys.length + 1);
  const spacing = Math.max(0, gap);
  offsets[0] = 0;
  for (let index = 0; index < keys.length; index += 1) {
    // The gap belongs BETWEEN rows, so the last row does not pad the scroll height.
    const trailingGap = index < keys.length - 1 ? spacing : 0;
    offsets[index + 1] = offsets[index]! + Math.max(0, measured.get(keys[index]!) ?? estimate) + trailingGap;
  }
  return offsets;
}

/** Largest row index whose top edge is at or above `offset`. */
function rowAtOrBefore(offsets: readonly number[], offset: number): number {
  const lastRow = offsets.length - 2;
  if (lastRow < 0) return 0;
  if (offset <= 0) return 0;
  let low = 0;
  let high = lastRow;
  while (low < high) {
    const middle = Math.ceil((low + high) / 2);
    if (offsets[middle]! <= offset) low = middle;
    else high = middle - 1;
  }
  return low;
}

/**
 * The rows intersecting [scrollTop, scrollTop + viewport), widened by `overscan` rows.
 * Returns [0, 0) for an empty list and always at least one row when the list is not
 * empty, so a zero-height viewport cannot render an empty scroll area.
 */
export function computeWindow(
  offsets: readonly number[],
  scrollTop: number,
  viewport: number,
  overscan: number,
): VirtualWindow {
  const rowCount = offsets.length - 1;
  if (rowCount <= 0) return { start: 0, end: 0 };
  const from = Math.max(0, scrollTop);
  const start = Math.max(0, rowAtOrBefore(offsets, from) - Math.max(0, overscan));
  const lastVisible = rowAtOrBefore(offsets, from + Math.max(0, viewport));
  const end = Math.min(rowCount, Math.max(start + 1, lastVisible + 1 + Math.max(0, overscan)));
  return { start, end };
}

function resolveElement(target: unknown): Element | null {
  if (!target) return null;
  if (target instanceof Element) return target;
  // `ref` on a component yields its public instance; the row box is the root element.
  const root = (target as { $el?: unknown }).$el;
  return root instanceof Element ? root : null;
}

export interface VirtualList<T> {
  /** Bind to the scrolling element. */
  containerRef: Ref<HTMLElement | null>;
  /** True when the row count exceeds the threshold and the window is active. */
  virtualized: Ref<boolean>;
  /** Rows to render, in order. */
  visible: Ref<T[]>;
  /** Index of `visible[0]` in the full list. */
  startIndex: Ref<number>;
  /** Space the rendered rows have to represent above / below themselves. */
  topSpacer: Ref<number>;
  bottomSpacer: Ref<number>;
  totalHeight: Ref<number>;
  onScroll: () => void;
  /** Stable per-row ref callback; the ResizeObserver measures the row it is attached to. */
  rowRef: (key: string) => (target: unknown) => void;
  /**
   * Test seam: record a measured row height. Production measurements arrive through
   * the ResizeObserver; happy-dom has no layout, so tests drive the same seam directly.
   */
  setRowHeight: (key: string, height: number) => void;
}

export function useVirtualList<T>(
  items: MaybeRefOrGetter<readonly T[]>,
  options: VirtualListOptions<T>,
): VirtualList<T> {
  const estimate = options.estimate ?? VIRTUAL_LIST_ESTIMATE_PX;
  const threshold = options.threshold ?? VIRTUAL_LIST_THRESHOLD;
  const overscan = options.overscan ?? VIRTUAL_LIST_OVERSCAN;
  const gap = options.gap ?? 0;

  const containerRef = ref<HTMLElement | null>(null);
  const scrollTop = ref(0);
  const viewport = ref(0);
  const measured = shallowRef<ReadonlyMap<string, number>>(new Map());

  const list = computed(() => toValue(items));
  const keys = computed(() => list.value.map((item) => options.keyOf(item)));
  const indexByKey = computed(() => {
    const byKey = new Map<string, number>();
    keys.value.forEach((key, index) => {
      if (!byKey.has(key)) byKey.set(key, index);
    });
    return byKey;
  });
  const virtualized = computed(() => list.value.length > threshold);
  const offsets = computed(() => buildOffsets(keys.value, measured.value, estimate, gap));
  const totalHeight = computed(() => offsets.value[offsets.value.length - 1] ?? 0);
  const effectiveViewport = computed(() => {
    if (viewport.value > 0) return viewport.value;
    return options.viewportFallback ?? estimate * 10;
  });
  const range = computed<VirtualWindow>(() => {
    if (!virtualized.value) return { start: 0, end: list.value.length };
    return computeWindow(offsets.value, scrollTop.value, effectiveViewport.value, overscan);
  });
  const visible = computed(() => list.value.slice(range.value.start, range.value.end));
  const startIndex = computed(() => range.value.start);
  const topSpacer = computed(() => (virtualized.value ? offsets.value[range.value.start] ?? 0 : 0));
  const bottomSpacer = computed(() =>
    virtualized.value ? Math.max(0, totalHeight.value - (offsets.value[range.value.end] ?? totalHeight.value)) : 0,
  );

  function onScroll(): void {
    const element = containerRef.value;
    if (!element) return;
    scrollTop.value = element.scrollTop;
    if (element.clientHeight > 0) viewport.value = element.clientHeight;
  }

  function setRowHeight(key: string, height: number): void {
    if (!Number.isFinite(height) || height <= 0) return;
    const index = indexByKey.value.get(key);
    if (index === undefined) return;
    const previous = measured.value.get(key);
    if (previous !== undefined && Math.abs(previous - height) < 0.5) return;
    // An unmeasured row occupied `estimate` before; the delta is relative to that.
    const shift = height - (previous ?? estimate);
    const next = new Map(measured.value);
    next.set(key, height);
    measured.value = next;
    // A row above the viewport grew or shrank: every rendered row moved by `shift`,
    // so move the scroll offset by the same amount to keep the content in place.
    if (shift !== 0 && virtualized.value && index < range.value.start && containerRef.value) {
      containerRef.value.scrollTop += shift;
      scrollTop.value = containerRef.value.scrollTop;
    }
  }

  const observed = new Map<string, { element: Element; observer: ResizeObserver }>();

  function observeRow(key: string, target: unknown): void {
    const element = resolveElement(target);
    const existing = observed.get(key);
    if (existing && existing.element === element) return;
    if (existing) {
      existing.observer.disconnect();
      observed.delete(key);
    }
    if (!element || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(() => {
      setRowHeight(key, element.getBoundingClientRect().height);
    });
    observer.observe(element);
    observed.set(key, { element, observer });
  }

  const rowRefCallbacks = new Map<string, (target: unknown) => void>();

  function rowRef(key: string): (target: unknown) => void {
    let callback = rowRefCallbacks.get(key);
    if (!callback) {
      callback = (target: unknown) => {
        observeRow(key, target);
      };
      rowRefCallbacks.set(key, callback);
    }
    return callback;
  }

  let containerObserver: ResizeObserver | undefined;
  let resizeFallback: (() => void) | undefined;

  function syncViewport(): void {
    const element = containerRef.value;
    if (!element) return;
    scrollTop.value = element.scrollTop;
    if (element.clientHeight > 0) viewport.value = element.clientHeight;
  }

  // The scrolling element can appear after mount (an empty list renders a state
  // component first), so the observer follows the ref instead of a one-shot onMounted.
  watch(
    containerRef,
    (element) => {
      containerObserver?.disconnect();
      containerObserver = undefined;
      if (!element) return;
      syncViewport();
      if (typeof ResizeObserver !== 'undefined') {
        containerObserver = new ResizeObserver(() => syncViewport());
        containerObserver.observe(element);
      } else if (typeof window !== 'undefined') {
        resizeFallback = () => syncViewport();
        window.addEventListener('resize', resizeFallback);
      }
    },
    { flush: 'post' },
  );

  // A live list can grow at the front (the timeline prepends newer entries). Without
  // this the rows under the viewport would jump down by the inserted height on every
  // event; instead move the scroll offset with them. At the very top no correction is
  // wanted: showing the new entries is the intended behavior.
  let previousKeys: readonly string[] = keys.value;

  watch(keys, (current) => {
    const previous = previousKeys;
    previousKeys = current;
    if (!virtualized.value || !containerRef.value || scrollTop.value <= 0 || previous.length === 0) return;
    const anchor = previous[0]!;
    const anchorIndex = indexByKey.value.get(anchor);
    if (anchorIndex === undefined || anchorIndex === 0) return;
    let shift = 0;
    for (let index = 0; index < anchorIndex; index += 1) {
      shift += (measured.value.get(current[index]!) ?? estimate) + gap;
    }
    if (shift <= 0) return;
    containerRef.value.scrollTop += shift;
    scrollTop.value = containerRef.value.scrollTop;
  });

  // A shrinking list (a filter or a cleared operation) must not leave the viewport
  // scrolled past the new end, where the window would compute zero rows.
  watch(totalHeight, (height) => {
    const element = containerRef.value;
    if (!element || !virtualized.value) return;
    const maximum = Math.max(0, height - 1);
    if (element.scrollTop > maximum) {
      element.scrollTop = maximum;
      scrollTop.value = element.scrollTop;
    }
  });

  onBeforeUnmount(() => {
    for (const { observer } of observed.values()) observer.disconnect();
    observed.clear();
    containerObserver?.disconnect();
    if (resizeFallback) window.removeEventListener('resize', resizeFallback);
  });

  return {
    containerRef,
    virtualized,
    visible,
    startIndex,
    topSpacer,
    bottomSpacer,
    totalHeight,
    onScroll,
    rowRef,
    setRowHeight,
  };
}
