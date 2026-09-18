import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import AuthorizationStaleNotice from './AuthorizationStaleNotice.vue';

describe('AuthorizationStaleNotice (REQ-033 AC-033-10)', () => {
  it('explains the disabled write entry when the snapshot is not fresh', () => {
    const wrapper = mount(AuthorizationStaleNotice, { props: { stale: true } });

    const notice = wrapper.get('[role="status"]');
    expect(notice.text()).toContain('授权数据未同步');
    // The server stays the final authority; the notice must not imply the UI is
    // the enforcement point (ADR-006 fail closed).
    expect(notice.text()).toContain('服务端');
  });

  it('renders nothing once the snapshot is fresh', () => {
    const wrapper = mount(AuthorizationStaleNotice, { props: { stale: false } });

    expect(wrapper.find('[role="status"]').exists()).toBe(false);
  });
});
