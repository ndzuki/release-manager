import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import BindingRevokeDialog from './BindingRevokeDialog.vue';

/*
 * TASK-363: the revoke confirmation, pinned as a presentational contract.
 *
 * AppDialog teleports to <body>, so queries go to the document (the same approach
 * the page tests use). The component renders an always-open danger alertdialog;
 * the page decides when it exists, and the reason must live INSIDE the panel
 * because the page-level <ErrorState> is behind the backdrop.
 */
const mounted: Array<{ unmount: () => void }> = [];

afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
});

function mountDialog(props: { description?: string; failure?: string | null; saving?: boolean } = {}) {
  const wrapper = mount(BindingRevokeDialog, {
    props: {
      description: '确认后将立即撤销客户 customer-a 的绑定，并收回该绑定授予的能力，此操作不可恢复。',
      failure: null,
      saving: false,
      ...props,
    },
  });
  mounted.push(wrapper);
  return wrapper;
}

function panel(): HTMLElement {
  const match = document.querySelector<HTMLElement>('[role="alertdialog"]');
  if (!match) throw new Error('revoke confirmation not rendered');
  return match;
}

function clickButton(root: ParentNode, label: string): void {
  const button = Array.from(root.querySelectorAll('button')).find((element) => element.textContent?.trim() === label);
  if (!button) throw new Error(`dialog button not found: ${label}`);
  button.click();
}

describe('BindingRevokeDialog', () => {
  it('renders the danger confirmation with its title and description', () => {
    mountDialog();

    const dialog = panel();
    expect(dialog.classList).toContain('app-dialog__panel--danger');
    expect(document.getElementById(dialog.getAttribute('aria-labelledby') ?? '')?.textContent).toBe('撤销绑定');
    expect(dialog.querySelector('.app-dialog__description')?.textContent).toContain('customer-a');
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    expect(dialog.getAttribute('tabindex')).toBe('-1');
  });

  it('renders the failure inside the panel with its testid and alert role', () => {
    mountDialog({ failure: '操作失败，请稍后重试' });

    const failure = panel().querySelector<HTMLElement>('[data-testid="revoke-failure"]');
    expect(failure?.textContent?.trim()).toBe('操作失败，请稍后重试');
    expect(failure?.getAttribute('role')).toBe('alert');
  });

  it('emits confirm from the footer button and close from cancel', () => {
    const wrapper = mountDialog();

    clickButton(panel(), '确认撤销');
    clickButton(panel(), '取消');

    expect(wrapper.emitted('confirm')).toHaveLength(1);
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('closes on Escape when idle and stays open while saving', async () => {
    const idle = mountDialog();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await flushPromises();
    expect(idle.emitted('close')).toHaveLength(1);
    idle.unmount();

    const saving = mountDialog({ saving: true });
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await flushPromises();
    expect(saving.emitted('close')).toBeUndefined();
  });

  it('disables both footer buttons and shows the in-flight label while saving', () => {
    mountDialog({ saving: true });

    const buttons = Array.from(panel().querySelectorAll<HTMLButtonElement>('button'));
    expect(buttons.map((button) => button.textContent?.trim())).toEqual(['取消', '撤销中…']);
    expect(buttons.every((button) => button.disabled)).toBe(true);
  });
});
