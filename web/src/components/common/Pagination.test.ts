import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import { nextTick } from 'vue';
import Pagination from './Pagination.vue';

/*
 * Pagination's contract (TASK-283 AC-283-02).
 *
 * The primitive is small on purpose, so the assertions pin the four properties that
 * make it safe to reuse instead of hand-rolling a pager:
 *
 *   - a disabled direction is disabled with the NATIVE attribute and still fires
 *     nothing. Native `disabled` blocks a real browser's activation but NOT a
 *     synthetic dispatch (happy-dom reaches the listener), so the no-emit assertions
 *     are what prove the guard inside the component, not the attribute alone;
 *   - a reachable direction emits exactly once — a double emit would advance the
 *     caller's cursor twice on one click;
 *   - the region carries an accessible name (the caller's, or the catalog default);
 *   - use `disabled`, never `aria-disabled`: a control that only looks disabled is
 *     still activatable, and no assertion here would be able to tell the difference.
 */
function mountPagination(overrides: Record<string, unknown> = {}) {
  return mount(Pagination, {
    props: { hasPrev: false, hasNext: false, ...overrides },
  });
}

function buttons(wrapper: ReturnType<typeof mountPagination>) {
  return wrapper.findAll('button');
}

describe('Pagination', () => {
  it('renders both directions as native buttons inside the labelled nav', () => {
    const wrapper = mountPagination({ hasPrev: true, hasNext: true, label: 'Operation 分页' });

    const nav = wrapper.get('nav');
    expect(nav.attributes('aria-label')).toBe('Operation 分页');

    const [prev, next] = buttons(wrapper);
    // Native <button> is what gives Tab, Enter and Space for free; a <div> with a
    // click handler and a role would be a different, worse control.
    expect(prev!.element.tagName).toBe('BUTTON');
    expect(prev!.attributes('type')).toBe('button');
    expect(next!.element.tagName).toBe('BUTTON');
    expect(next!.attributes('type')).toBe('button');
    expect(prev!.text()).toBe('上一页');
    expect(next!.text()).toBe('下一页');
  });

  it('falls back to the catalog label when the caller passes none', () => {
    const wrapper = mountPagination();

    expect(wrapper.get('nav').attributes('aria-label')).toBe('分页');
  });

  it('disables an unreachable direction and emits nothing from it', async () => {
    const wrapper = mountPagination({ hasPrev: false, hasNext: false });
    const [prev, next] = buttons(wrapper);

    expect(prev!.attributes('disabled')).toBeDefined();
    expect(next!.attributes('disabled')).toBeDefined();
    // The anti-pattern this pins: `aria-disabled` hides the control from the native
    // activation rules while leaving it clickable.
    expect(prev!.attributes('aria-disabled')).toBeUndefined();
    expect(next!.attributes('aria-disabled')).toBeUndefined();

    /*
     * `trigger('click')` is not enough here: VTU skips the dispatch on a disabled
     * element entirely, and happy-dom blocks a synthetic MouseEvent the same way, so
     * a deleted in-component guard would stay invisible to that path. A plain
     * `Event('click')` DOES reach the listener in happy-dom (and a queued or
     * programmatic dispatch does in a real browser), which is exactly the input the
     * guard exists to reject.
     */
    prev!.element.dispatchEvent(new Event('click'));
    next!.element.dispatchEvent(new Event('click'));
    await nextTick();

    expect(wrapper.emitted('prev')).toBeUndefined();
    expect(wrapper.emitted('next')).toBeUndefined();

    // The ordinary input path, for completeness: no emit from a disabled control.
    await prev!.trigger('click');
    await next!.trigger('click');
    expect(wrapper.emitted('prev')).toBeUndefined();
    expect(wrapper.emitted('next')).toBeUndefined();
  });

  it('keeps a disabled control in the DOM so the tab order cannot shift', () => {
    const wrapper = mountPagination({ hasPrev: false, hasNext: true });

    // Not v-if: the button stays where it is and only its state changes, which is
    // what stops focus from jumping when a page boundary is reached.
    expect(buttons(wrapper)).toHaveLength(2);
    expect(buttons(wrapper)[0]!.attributes('disabled')).toBeDefined();
  });

  it('emits each direction exactly once when it is reachable', async () => {
    const wrapper = mountPagination({ hasPrev: true, hasNext: true });
    const [prev, next] = buttons(wrapper);

    await next!.trigger('click');
    await prev!.trigger('click');

    expect(wrapper.emitted('next')).toHaveLength(1);
    expect(wrapper.emitted('prev')).toHaveLength(1);
  });

  it('shows the 1-based page indicator only when a page is given', () => {
    const withPage = mountPagination({ page: 3 });
    expect(withPage.get('.pagination__page').text()).toBe('第 3 页');
    // role="status" is what announces the page change without stealing focus.
    expect(withPage.get('.pagination__page').attributes('role')).toBe('status');

    expect(mountPagination().find('.pagination__page').exists()).toBe(false);
  });

  it('does not move focus when its props change', async () => {
    const wrapper = mount(Pagination, {
      attachTo: document.body,
      props: { hasPrev: true, hasNext: true },
    });
    const prev = wrapper.get<HTMLButtonElement>('button');
    prev.element.focus();
    expect(document.activeElement).toBe(prev.element);

    // Reaching the last page disables "next"; the focused control must keep focus.
    await wrapper.setProps({ hasNext: false });
    expect(document.activeElement).toBe(prev.element);

    wrapper.unmount();
  });
});
