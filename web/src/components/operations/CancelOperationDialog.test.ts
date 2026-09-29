import { describe, expect, it } from 'vitest';
import { flushPromises, mount } from '@vue/test-utils';
import CancelOperationDialog from './CancelOperationDialog.vue';

// The overlay is teleported by AppDialog, so these tests stub Teleport to keep
// the dialog's own behaviour in the wrapper. The overlay itself (real Teleport,
// focus trap, Escape at document level) is AppDialog.test.ts's subject.
function mountDialog(props: Record<string, unknown> = {}) {
  return mount(CancelOperationDialog, {
    global: { stubs: { Teleport: true } },
    props,
  });
}

describe('CancelOperationDialog', () => {
  it('validates reason length with Unicode code points', async () => {
    const wrapper = mountDialog({ submitting: false });
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
    const wrapper = mountDialog({ submitting: false, emergencyQueued: true });
    expect(wrapper.text()).toContain('取消不等于 K8s 回滚');
  });

  it('AC-22: keeps the dialog open with the error inline on failure', async () => {
    const wrapper = mountDialog({
      submitting: false,
      error: { code: 'cancel_not_allowed', message: '当前状态不允许取消' },
    });
    await flushPromises();
    expect(wrapper.text()).toContain('当前状态不允许取消');
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true);
  });

  it('emits close on Esc (document level) and on a backdrop click', async () => {
    const wrapper = mountDialog({ submitting: false });

    // Escape is handled by the focus trap's document listener, so it works even
    // when focus is not inside the panel.
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(wrapper.emitted('close')).toHaveLength(1);

    await wrapper.find('.app-dialog').trigger('click');
    expect(wrapper.emitted('close')).toHaveLength(2);
  });
});
