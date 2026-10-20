import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import StatusTag from './StatusTag.vue';

/*
 * StatusTag contract (TASK-394): a polite live region (the state can change under the
 * reader), the tone drives ONE custom property the stylesheet mixes from, and the pulse
 * is opt-in. The accessible name must be overridable without changing the visible label.
 */
describe('StatusTag', () => {
  it('renders the label in a polite status region', () => {
    const wrapper = mount(StatusTag, { props: { tone: 'ok', label: '生效中' } });

    expect(wrapper.get('.status-tag').attributes('role')).toBe('status');
    expect(wrapper.get('.status-tag__label').text()).toBe('生效中');
    expect(wrapper.get('.status-tag').classes()).toContain('status-tag--ok');
  });

  it('maps each tone onto its own modifier class', () => {
    // The colour itself is CSS (currentColor + color-mix); the class is the contract a
    // caller and the stylesheet agree on, and it is what a broken tone would change.
    for (const tone of ['ok', 'wn', 'er', 'neutral', 'info'] as const) {
      const wrapper = mount(StatusTag, { props: { tone, label: 'x' } });
      expect(wrapper.get('.status-tag').classes()).toContain(`status-tag--${tone}`);
    }
  });

  it('only animates the dot when pulse is requested', () => {
    const still = mount(StatusTag, { props: { tone: 'ok', label: 'x' } });
    expect(still.find('.status-tag__dot--pulse').exists()).toBe(false);

    const pulsing = mount(StatusTag, { props: { tone: 'info', label: 'x', pulse: true } });
    expect(pulsing.find('.status-tag__dot--pulse').exists()).toBe(true);
  });

  it('lets the caller replace the accessible name', () => {
    const wrapper = mount(StatusTag, {
      props: { tone: 'er', label: '失败', ariaLabel: 'Preflight 失败' },
    });

    expect(wrapper.get('.status-tag').attributes('aria-label')).toBe('Preflight 失败');
    expect(wrapper.text()).toBe('失败');
  });

  it('omits aria-label when the caller does not pass one', () => {
    const wrapper = mount(StatusTag, { props: { tone: 'neutral', label: 'x' } });

    expect(wrapper.get('.status-tag').attributes('aria-label')).toBeUndefined();
  });
});
