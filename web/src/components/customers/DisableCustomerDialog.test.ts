import { describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import DisableCustomerDialog from './DisableCustomerDialog.vue';
import { t } from '@/i18n/messages';

// The overlay is teleported by AppDialog, so these tests stub Teleport to keep the
// dialog's own state in the wrapper. The overlay itself (real Teleport, focus trap,
// Escape at document level) is AppDialog.test.ts's subject.
function mountDialog() {
  return mount(DisableCustomerDialog, {
    global: { stubs: { Teleport: true } },
    props: { open: true },
  });
}

function isAcknowledged(wrapper: ReturnType<typeof mountDialog>): boolean {
  return (wrapper.find('input[type="checkbox"]').element as HTMLInputElement).checked;
}

function confirmButton(wrapper: ReturnType<typeof mountDialog>) {
  return wrapper.find('.disable-dialog__danger');
}

describe('DisableCustomerDialog', () => {
  /*
   * UX-021. The owner keeps this dialog mounted and drives it with `:open` only —
   * CustomerDetailPage closes it straight after a successful disable, without going
   * through `cancel()` — so "does the acknowledgement survive a close?" is a property
   * of this component rather than of the page that hosts it. A stale tick would make
   * the next disable one click away, with the impact list already acknowledged.
   */
  it('requires the acknowledgement again on every open (UX-021)', async () => {
    const wrapper = mountDialog();

    expect(isAcknowledged(wrapper)).toBe(false);
    expect(confirmButton(wrapper).attributes('disabled')).toBeDefined();

    await wrapper.find('input[type="checkbox"]').setValue(true);
    expect(isAcknowledged(wrapper)).toBe(true);
    expect(confirmButton(wrapper).attributes('disabled')).toBeUndefined();

    // Closed by the owner after a successful disable: flip the prop, not `cancel`.
    await wrapper.setProps({ open: false });
    await wrapper.setProps({ open: true });

    expect(isAcknowledged(wrapper)).toBe(false);
    expect(confirmButton(wrapper).attributes('disabled')).toBeDefined();
  });

  it('drops the acknowledgement when the user cancels', async () => {
    const wrapper = mountDialog();
    await wrapper.find('input[type="checkbox"]').setValue(true);

    const cancel = wrapper.findAll('button').find((button) => button.text() === t('action.cancel'))!;
    await cancel.trigger('click');

    expect(wrapper.emitted('cancel')).toHaveLength(1);
    expect(isAcknowledged(wrapper)).toBe(false);
  });
});
