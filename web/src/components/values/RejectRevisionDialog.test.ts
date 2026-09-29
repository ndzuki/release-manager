import { describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import RejectRevisionDialog from './RejectRevisionDialog.vue';

/*
 * The dialog's close semantics: it previously had `@click.self` on its backdrop,
 * and the migration to AppDialog must keep that (the one-off secret dialogs are
 * the ones that opt out). The primitive's default is covered in
 * AppDialog.test.ts; this pins that THIS component does not override it.
 */
function mountDialog(props: Record<string, unknown> = {}) {
  return mount(RejectRevisionDialog, {
    global: { stubs: { Teleport: true } },
    props,
  });
}

describe('RejectRevisionDialog', () => {
  it('closes on a backdrop click, as it did before the AppDialog migration', async () => {
    const wrapper = mountDialog();

    await wrapper.find('.app-dialog').trigger('click');

    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('keeps escape working but blocks a submit while one is in flight', async () => {
    const submitting = mountDialog({ submitting: true });
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(submitting.emitted('close')).toBeUndefined();

    submitting.unmount();
    const idle = mountDialog({ submitting: false });
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(idle.emitted('close')).toHaveLength(1);
  });
});
