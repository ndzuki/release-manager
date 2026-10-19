import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import DisconnectBanner from './DisconnectBanner.vue';

/*
 * UX-045: the banner used to declare `role="status"` (implicitly polite) together with
 * `aria-live="assertive"`. The explicit attribute wins at runtime, so the banner
 * interrupted the reader while the role claimed it would not. The politeness is now
 * declared once, politely, for the reason recorded in the component.
 */
describe('DisconnectBanner live-region contract (REQ-057 item 11, UX-045)', () => {
  it('announces a reconnect politely rather than asserting', () => {
    const wrapper = mount(DisconnectBanner, { props: { visible: true } });

    const banner = wrapper.get('.disconnect-banner');
    expect(banner.attributes('role')).toBe('status');
    expect(banner.attributes('aria-live')).toBe('polite');
    expect(banner.text()).toContain('正在重连');
  });

  it('renders nothing while the stream is connected', () => {
    const wrapper = mount(DisconnectBanner, { props: { visible: false } });

    expect(wrapper.find('.disconnect-banner').exists()).toBe(false);
  });
});
