import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import Alert from './Alert.vue';

/*
 * Alert contract (TASK-394). The role is the accessibility decision that matters: an
 * error or a warning interrupts (`alert`), while info and success wait for a pause
 * (`status`). The message is optional so the component can be a one-line banner, and the
 * actions slot is what keeps callers from nesting a second ad-hoc layout next to it.
 */
describe('Alert', () => {
  it('uses assertive announcements for error and warning', () => {
    for (const tone of ['error', 'warning'] as const) {
      const wrapper = mount(Alert, { props: { tone, title: 'x' } });
      expect(wrapper.get('.alert').attributes('role'), tone).toBe('alert');
    }
  });

  it('uses polite announcements for info and success', () => {
    for (const tone of ['info', 'success'] as const) {
      const wrapper = mount(Alert, { props: { tone, title: 'x' } });
      expect(wrapper.get('.alert').attributes('role'), tone).toBe('status');
    }
  });

  it('renders title, message and the tone class', () => {
    const wrapper = mount(Alert, { props: { tone: 'error', title: '操作未成功', message: '服务端拒绝了请求' } });

    expect(wrapper.get('.alert').classes()).toContain('alert--error');
    expect(wrapper.get('.alert__title').text()).toBe('操作未成功');
    expect(wrapper.get('.alert__message').text()).toBe('服务端拒绝了请求');
  });

  it('omits the message paragraph when there is no message', () => {
    const wrapper = mount(Alert, { props: { tone: 'info', title: '仅标题' } });

    expect(wrapper.find('.alert__message').exists()).toBe(false);
  });

  it('renders the actions slot', () => {
    const wrapper = mount(Alert, {
      props: { tone: 'warning', title: '需要确认' },
      slots: { actions: '<button type="button">重试</button>' },
    });

    expect(wrapper.get('.alert__actions').text()).toBe('重试');
  });
});
