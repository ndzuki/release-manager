import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import Steps from './Steps.vue';

/*
 * Steps contract (TASK-394): the flow is an ordered list, exactly one step can be the
 * current one, and `aria-current="step"` — not a colour — is what tells assistive
 * technology where the reader is. The pulse and the check mark are decoration, so both
 * live in CSS and are hidden from the accessibility tree.
 */
const steps = [
  { label: '选择制品', state: 'done' as const },
  { label: '确认目标', state: 'now' as const },
  { label: '执行', state: 'todo' as const },
];

describe('Steps', () => {
  it('renders an ordered list with one item per step', () => {
    const wrapper = mount(Steps, { props: { steps } });

    expect(wrapper.element.tagName).toBe('OL');
    expect(wrapper.findAll('li')).toHaveLength(3);
    expect(wrapper.findAll('li').map((item) => item.text())).toEqual(['选择制品', '确认目标', '执行']);
  });

  it('marks exactly the current step', () => {
    const wrapper = mount(Steps, { props: { steps } });

    const current = wrapper.findAll('[aria-current="step"]');
    expect(current).toHaveLength(1);
    expect(current[0]!.text()).toBe('确认目标');
    expect(current[0]!.classes()).toContain('steps__item--now');
  });

  it('exposes no current step when none is now', () => {
    const wrapper = mount(Steps, {
      props: { steps: steps.map((step) => ({ ...step, state: 'done' as const })) },
    });

    expect(wrapper.findAll('[aria-current="step"]')).toHaveLength(0);
  });

  it('hides the dot decoration from assistive technology', () => {
    const wrapper = mount(Steps, { props: { steps } });

    for (const dot of wrapper.findAll('.steps__dot')) {
      expect(dot.attributes('aria-hidden')).toBe('true');
    }
  });
});
