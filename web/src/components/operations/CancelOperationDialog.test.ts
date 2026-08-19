import { describe, expect, it } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import CancelOperationDialog from './CancelOperationDialog.vue';

describe('CancelOperationDialog', () => {
  it('validates reason length with Unicode code points', async () => {
    const wrapper = mount(CancelOperationDialog, { props: { submitting: false } });
    const textarea = wrapper.find('textarea');
    const confirm = wrapper.findAll('button').find((button) => button.text() === '确认取消')!;

    // Empty after trim: block submit.
    await textarea.setValue('   ');
    await confirm.trigger('click');
    expect(wrapper.emitted('submit')).toBeUndefined();
    expect(wrapper.text()).toContain('取消原因不能为空');

    // Over 500 Unicode characters: block submit.
    await textarea.setValue('字'.repeat(501));
    expect(wrapper.text()).toContain('取消原因过长');
    await confirm.trigger('click');
    expect(wrapper.emitted('submit')).toBeUndefined();

    // Valid: emits trimmed reason.
    await textarea.setValue('  业务原因  ');
    await confirm.trigger('click');
    expect(wrapper.emitted('submit')).toEqual([['业务原因']]);
  });

  it('shows the emergency queued note when requested', () => {
    const wrapper = mount(CancelOperationDialog, { props: { submitting: false, emergencyQueued: true } });
    expect(wrapper.text()).toContain('取消不等于 K8s 回滚');
  });

  it('keeps the dialog open with the error inline on failure', async () => {
    const wrapper = mount(CancelOperationDialog, {
      props: { submitting: false, error: { code: 'cancel_not_allowed', message: '当前状态不允许取消' } },
    });
    await flushPromises();
    expect(wrapper.text()).toContain('当前状态不允许取消');
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true);
  });

  it('emits close on Esc and backdrop click', async () => {
    const wrapper = mount(CancelOperationDialog, { props: { submitting: false } });
    await wrapper.find('[role="dialog"]').trigger('keydown.esc');
    expect(wrapper.emitted('close')).toHaveLength(1);

    await wrapper.find('.dialog-backdrop').trigger('click');
    expect(wrapper.emitted('close')).toHaveLength(2);
  });
});
