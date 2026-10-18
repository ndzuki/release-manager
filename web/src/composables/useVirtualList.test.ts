import { mount } from '@vue/test-utils';
import { defineComponent, h, nextTick } from 'vue';
import { describe, expect, it } from 'vitest';
import {
  buildOffsets,
  computeWindow,
  useVirtualList,
  VIRTUAL_LIST_THRESHOLD,
  type VirtualList,
} from './useVirtualList';

interface Row {
  id: string;
}

function rows(count: number): Row[] {
  return Array.from({ length: count }, (_, index) => ({ id: `row-${index}` }));
}

describe('buildOffsets', () => {
  it('is empty for an empty list', () => {
    expect(buildOffsets([], new Map(), 40)).toEqual([0]);
  });

  it('falls back to the estimate and adds the gap between rows', () => {
    // 3 rows: 40 + 8, 40 + 8, 40 + 8 -> cumulative, with no trailing gap after the last row
    expect(buildOffsets(['a', 'b', 'c'], new Map([['b', 100]]), 40, 8)).toEqual([0, 48, 156, 196]);
  });
});

describe('computeWindow', () => {
  // 10 rows of 50px, no gap.
  const offsets = Array.from({ length: 11 }, (_, index) => index * 50);

  it('returns an empty window for an empty list', () => {
    expect(computeWindow([0], 0, 100, 2)).toEqual({ start: 0, end: 0 });
  });

  it('renders from the top with only the trailing overscan', () => {
    expect(computeWindow(offsets, 0, 200, 2)).toEqual({ start: 0, end: 7 });
  });

  it('keeps overscan rows above the viewport and drops the rest', () => {
    // scrollTop 250 starts row 5; 2 overscan rows -> row 3.
    expect(computeWindow(offsets, 250, 200, 2)).toEqual({ start: 3, end: 10 });
  });

  it('clamps at the end of the list', () => {
    expect(computeWindow(offsets, 500, 200, 2)).toEqual({ start: 7, end: 10 });
  });

  it('never overflows past the last row when scrolled beyond the height', () => {
    expect(computeWindow(offsets, 10_000, 200, 2)).toEqual({ start: 7, end: 10 });
  });

  it('keeps one row rendered when the viewport has no height yet', () => {
    // The first render happens before the container is measured; an empty scroll
    // area would then never fill.
    expect(computeWindow(offsets, 0, 0, 0)).toEqual({ start: 0, end: 1 });
  });

  it('treats a negative scroll offset as the top', () => {
    expect(computeWindow(offsets, -40, 100, 0)).toEqual({ start: 0, end: 3 });
  });
});

describe('useVirtualList', () => {
  let virtual: VirtualList<Row>;

  const Host = defineComponent({
    props: {
      items: { type: Array, required: true },
      threshold: { type: Number, default: 4 },
    },
    setup(props) {
      virtual = useVirtualList(() => props.items as Row[], {
        keyOf: (row) => row.id,
        estimate: 50,
        threshold: props.threshold,
        overscan: 2,
        gap: 0,
        viewportFallback: 200,
      });
      return () =>
        h(
          'ol',
          { ref: virtual.containerRef, onScroll: virtual.onScroll },
          virtual.visible.value.map((row) =>
            h('li', { key: row.id, ref: virtual.rowRef(row.id) }, row.id),
          ),
        );
    },
  });

  function mountHost(count: number, threshold = 4) {
    return mount(Host, { props: { items: rows(count), threshold } });
  }

  it('renders every row below the threshold and reports no spacers', () => {
    const wrapper = mountHost(4);
    expect(virtual.virtualized.value).toBe(false);
    expect(wrapper.findAll('li')).toHaveLength(4);
    expect(virtual.topSpacer.value).toBe(0);
    expect(virtual.bottomSpacer.value).toBe(0);
    expect(virtual.startIndex.value).toBe(0);
  });

  it('windows a long list and keeps the total height stable', () => {
    const wrapper = mountHost(100);
    expect(virtual.virtualized.value).toBe(true);
    // viewport 200 / estimate 50 => rows 0..6 (the row starting at 200 plus overscan)
    expect(wrapper.findAll('li')).toHaveLength(7);
    expect(virtual.topSpacer.value).toBe(0);
    // 100 rows * 50px = 5000; the 7 rendered rows represent 350px at the top
    expect(virtual.totalHeight.value).toBe(5000);
    expect(virtual.bottomSpacer.value).toBe(4650);
  });

  it('advances the window on scroll and keeps rendered rows inside the list', async () => {
    const wrapper = mountHost(100);
    const list = wrapper.get('ol');
    list.element.scrollTop = 1000;
    await wrapper.trigger('scroll');
    await nextTick();

    expect(virtual.startIndex.value).toBe(18);
    // rows 18..26 => 9 rendered rows (the viewport starting at row 20 plus overscan)
    expect(wrapper.findAll('li')).toHaveLength(9);
    expect(wrapper.findAll('li')[0]!.text()).toBe('row-18');
    expect(wrapper.findAll('li').at(-1)!.text()).toBe('row-26');
    expect(virtual.topSpacer.value).toBe(900);
    expect(virtual.bottomSpacer.value).toBe(5000 - 1350);
  });

  it('applies measured heights and keeps a scroll anchor when a row above the viewport grows', async () => {
    const wrapper = mountHost(100);
    const list = wrapper.get('ol');
    list.element.scrollTop = 1000;
    await wrapper.trigger('scroll');

    // Row 5 is above the window (start 18): its height change shifts every later row,
    // so the scroll offset must move with it to keep the visible rows in place.
    virtual.setRowHeight('row-5', 80);
    await nextTick();

    expect(virtual.topSpacer.value).toBe(930);
    expect(list.element.scrollTop).toBe(1030);
    // The rendered rows did not change: the anchor correction happened instead.
    expect(wrapper.findAll('li')[0]!.text()).toBe('row-18');
  });

  it('does not move the scroll offset when a row inside the window is measured', async () => {
    const wrapper = mountHost(100);
    const list = wrapper.get('ol');
    list.element.scrollTop = 1000;
    await wrapper.trigger('scroll');

    virtual.setRowHeight('row-20', 70);
    await nextTick();

    expect(list.element.scrollTop).toBe(1000);
    expect(virtual.totalHeight.value).toBe(5020);
  });

  it('renders nothing for an empty list', () => {
    const wrapper = mountHost(0);
    expect(wrapper.findAll('li')).toHaveLength(0);
    expect(virtual.virtualized.value).toBe(false);
    expect(virtual.totalHeight.value).toBe(0);
  });

  it('clamps a stale scroll offset when the list shrinks below it', async () => {
    const wrapper = await mountHost(100);
    const list = wrapper.get('ol');
    list.element.scrollTop = 4000;
    await wrapper.trigger('scroll');
    expect(virtual.startIndex.value).toBeGreaterThan(0);

    await wrapper.setProps({ items: rows(10) });
    await nextTick();

    // 10 rows * 50px = 500: the offset can no longer point past the new end.
    expect(list.element.scrollTop).toBeLessThan(500);
    expect(virtual.visible.value.length).toBeGreaterThan(0);
  });

  it('keeps the viewport over the same row when newer rows are prepended', async () => {
    const original = rows(100);
    const wrapper = mount(Host, { props: { items: original, threshold: 4 } });
    const list = wrapper.get('ol');
    list.element.scrollTop = 1000;
    await wrapper.trigger('scroll');
    const firstBefore = wrapper.findAll('li')[0]!.text();
    expect(firstBefore).toBe('row-18');

    await wrapper.setProps({ items: [{ id: 'new-1' }, { id: 'new-2' }, { id: 'new-3' }, ...original] });
    await nextTick();

    // Three 50px rows were inserted above the viewport; the offset follows them so
    // the row the user was reading stays put (a live timeline prepends entries).
    expect(list.element.scrollTop).toBe(1150);
    expect(wrapper.findAll('li')[0]!.text()).toBe(firstBefore);
  });

  it('stays at the top when newer rows are prepended while the list is at the top', async () => {
    const original = rows(100);
    const wrapper = mount(Host, { props: { items: original, threshold: 4 } });
    const list = wrapper.get('ol');
    expect(list.element.scrollTop).toBe(0);

    await wrapper.setProps({ items: [{ id: 'new-1' }, ...original] });
    await nextTick();

    expect(list.element.scrollTop).toBe(0);
    expect(wrapper.findAll('li')[0]!.text()).toBe('new-1');
  });

  it('returns the same ref callback for the same row so measuring is not restarted', () => {
    mountHost(100);
    expect(virtual.rowRef('row-1')).toBe(virtual.rowRef('row-1'));
    expect(virtual.rowRef('row-1')).not.toBe(virtual.rowRef('row-2'));
  });

  it('uses 60 rows as the default virtualization threshold', () => {
    expect(VIRTUAL_LIST_THRESHOLD).toBe(60);
  });
});
