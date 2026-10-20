import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import StatCard from './StatCard.vue';

/*
 * StatCard contract (TASK-394): label + value always render, the hint is optional, and
 * the tone colours only the number. The value is a string OR a number — callers pass
 * pre-formatted counts — so both must render without Vue turning `0` into an empty node.
 */
describe('StatCard', () => {
  it('renders the label and the value', () => {
    const wrapper = mount(StatCard, { props: { label: '进行中操作', value: 12 } });

    expect(wrapper.get('.stat-card__label').text()).toBe('进行中操作');
    expect(wrapper.get('.stat-card__value').text()).toBe('12');
  });

  it('renders a zero value instead of dropping it', () => {
    const wrapper = mount(StatCard, { props: { label: '失败', value: 0 } });

    expect(wrapper.get('.stat-card__value').text()).toBe('0');
  });

  it('renders the hint only when one is supplied', () => {
    const withoutHint = mount(StatCard, { props: { label: 'a', value: 'b' } });
    expect(withoutHint.find('.stat-card__hint').exists()).toBe(false);

    const withHint = mount(StatCard, { props: { label: 'a', value: 'b', hint: '近 24 小时' } });
    expect(withHint.get('.stat-card__hint').text()).toBe('近 24 小时');
  });

  it('maps each tone onto its own modifier class', () => {
    // Scoped CSS is not injected into happy-dom, so the promise under test is the class
    // contract the stylesheet keys the value colour on, not a computed rgb value.
    for (const tone of ['neutral', 'ok', 'wn', 'er', 'info'] as const) {
      const wrapper = mount(StatCard, { props: { label: 'a', value: 1, tone } });
      expect(wrapper.get('.stat-card').classes()).toContain(`stat-card--${tone}`);
    }
  });
});
