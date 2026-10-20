import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import Timeline from './Timeline.vue';

/*
 * Timeline contract (TASK-394): an ordered list of happenings, each with a title and an
 * optional meta line, plus the `now` marker for the one that is still in flight. The rail
 * and the pulsing halo are CSS; the only structural promise is that `now` is reflected on
 * the dot so a reader (and a test) can tell the live item from the settled ones.
 */
const items = [
  { title: '操作已受理', meta: '12:01:04' },
  { title: '等待 Operator 确认', meta: '12:01:09', now: true },
  { title: '发布完成' },
];

describe('Timeline', () => {
  it('renders one list item per entry in order', () => {
    const wrapper = mount(Timeline, { props: { items } });

    expect(wrapper.element.tagName).toBe('OL');
    const list = wrapper.findAll('li');
    expect(list).toHaveLength(3);
    expect(list.map((item) => item.get('.timeline__title').text())).toEqual([
      '操作已受理',
      '等待 Operator 确认',
      '发布完成',
    ]);
  });

  it('renders the meta line only when supplied', () => {
    const wrapper = mount(Timeline, { props: { items } });
    const list = wrapper.findAll('li');

    expect(list[0]!.get('.timeline__meta').text()).toBe('12:01:04');
    expect(list[2]!.find('.timeline__meta').exists()).toBe(false);
  });

  it('marks the in-flight item on its dot', () => {
    const wrapper = mount(Timeline, { props: { items } });
    const dots = wrapper.findAll('.timeline__dot');

    expect(dots[1]!.classes()).toContain('timeline__dot--now');
    expect(dots[0]!.classes()).not.toContain('timeline__dot--now');
  });

  it('hides the rail and dots from assistive technology', () => {
    const wrapper = mount(Timeline, { props: { items } });

    for (const rail of wrapper.findAll('.timeline__rail')) {
      expect(rail.attributes('aria-hidden')).toBe('true');
    }
  });

  it('renders nothing but the empty list when there are no items', () => {
    const wrapper = mount(Timeline, { props: { items: [] } });

    expect(wrapper.findAll('li')).toHaveLength(0);
  });
});
