import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import EmptyState from './EmptyState.vue';
import ErrorState from './ErrorState.vue';
import ForbiddenState from './ForbiddenState.vue';
import LoadingState from './LoadingState.vue';

describe('common state components', () => {
  it('exposes an accessible loading status', () => {
    const wrapper = mount(LoadingState, { props: { message: 'Loading releases' } });

    expect(wrapper.get('[role="status"]').attributes('aria-label')).toBe('Loading');
    expect(wrapper.text()).toContain('Loading releases');
  });

  it('emits the reusable empty action contract', async () => {
    const wrapper = mount(EmptyState, { props: { title: 'Nothing here', actionLabel: 'Reload' } });

    await wrapper.get('button').trigger('click');

    expect(wrapper.emitted('action')).toHaveLength(1);
  });

  it('renders error details without HTML interpretation', () => {
    const wrapper = mount(ErrorState, {
      props: { message: 'Request failed', details: '<script>alert(1)</script>' },
    });

    expect(wrapper.get('[role="alert"]').text()).toContain('<script>alert(1)</script>');
    expect(wrapper.find('script').exists()).toBe(false);
  });

  it('emits the forbidden recovery action', async () => {
    const wrapper = mount(ForbiddenState, { props: { actionLabel: 'Go home' } });

    await wrapper.get('button').trigger('click');

    expect(wrapper.emitted('action')).toHaveLength(1);
  });
});
