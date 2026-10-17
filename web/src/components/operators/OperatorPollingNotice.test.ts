import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import OperatorPollingNotice from './OperatorPollingNotice.vue';
import { t } from '@/i18n/messages';

/*
 * TASK-325 (AC-325-05). The notice is the only user-visible half of the composable's
 * budget exhaustion, and the two assertions that carry the contract are the live-region
 * role and the emitted restore event: without the first a screen reader never hears the
 * stop, without the second the stop is a dead end.
 */
describe('OperatorPollingNotice', () => {
  it('renders nothing while the poll is running', () => {
    const wrapper = mount(OperatorPollingNotice, { props: { stopped: false } });

    expect(wrapper.find('[data-testid="operator-polling-stopped"]').exists()).toBe(false);
    expect(wrapper.text()).toBe('');
  });

  it('announces the stop politely and offers the restore path', async () => {
    const wrapper = mount(OperatorPollingNotice, { props: { stopped: false } });

    await wrapper.setProps({ stopped: true });
    const notice = wrapper.get('[data-testid="operator-polling-stopped"]');

    // `status` is the polite live region. Assertive `alert` would announce the same
    // failure twice, because the page's stale-data paragraph already carries it.
    expect(notice.attributes('role')).toBe('status');
    expect(notice.text()).toContain(t('operator.polling.stopped'));

    const resume = wrapper.get('[data-testid="operator-polling-resume"]');
    expect(resume.attributes('type')).toBe('button');
    expect(resume.text()).toBe(t('operator.polling.resume'));

    await resume.trigger('click');
    expect(wrapper.emitted('resume')).toHaveLength(1);

    // The caller owns the state; the notice only signals and never clears itself.
    await wrapper.setProps({ stopped: false });
    expect(wrapper.find('[data-testid="operator-polling-stopped"]').exists()).toBe(false);
  });
});
