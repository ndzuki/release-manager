import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import { nextTick } from 'vue';
import OrganizationDisableDialog from './OrganizationDisableDialog.vue';

/*
 * TASK-363: the disable-organization confirmation was carved out of
 * OrganizationPage. Disabling is terminal, so the confirmation is a `danger`
 * alertdialog and the RPC must run only from the confirm event — never from the
 * markup. AppDialog teleports to <body> (ADR-029), so the controls are queried in
 * the document.
 */

const mounted: Array<{ unmount: () => void }> = [];

afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
});

function mountDialog(
  overrides: { description?: string; failure?: string | null; saving?: boolean } = {},
) {
  const wrapper = mount(OrganizationDisableDialog, {
    props: {
      description: overrides.description ?? '确认后将停用组织 Platform：该组织的成员将无法再对其范围内的资源执行操作。',
      failure: overrides.failure ?? null,
      saving: overrides.saving ?? false,
    },
    attachTo: document.body,
  });
  mounted.push(wrapper);
  return wrapper;
}

function alertdialog(): HTMLElement {
  const matches = Array.from(document.querySelectorAll<HTMLElement>('[role="alertdialog"]'));
  if (matches.length !== 1) throw new Error(`expected exactly one alertdialog, found ${matches.length}`);
  return matches[0]!;
}

function clickButton(root: HTMLElement, label: string): void {
  const button = Array.from(root.querySelectorAll('button')).find(
    (element) => element.textContent?.trim() === label,
  );
  if (!button) throw new Error(`dialog button not found: ${label}`);
  button.click();
}

describe('OrganizationDisableDialog', () => {
  it('renders a danger alertdialog with the organization in its description', async () => {
    mountDialog();
    await nextTick();

    const dialog = alertdialog();
    expect(dialog.getAttribute('aria-modal')).toBe('true');
    const titleId = dialog.getAttribute('aria-labelledby');
    expect(document.getElementById(titleId ?? '')?.textContent).toBe('禁用组织');
    expect(dialog.querySelector('.app-dialog__description')?.textContent).toContain('Platform');
  });

  it('emits confirm only from the confirmation button', async () => {
    const wrapper = mountDialog();
    await nextTick();

    clickButton(alertdialog(), '确认停用');

    expect(wrapper.emitted('confirm')).toHaveLength(1);
    expect(wrapper.emitted('close')).toBeUndefined();
  });

  it('emits close from the cancel button', async () => {
    const wrapper = mountDialog();
    await nextTick();

    clickButton(alertdialog(), '取消');

    expect(wrapper.emitted('close')).toHaveLength(1);
    expect(wrapper.emitted('confirm')).toBeUndefined();
  });

  it('keeps the refusal reason inside the still-open dialog', async () => {
    mountDialog({ failure: '组织信息已被其他人修改，已为你刷新最新数据，请重试' });
    await nextTick();

    const failure = alertdialog().querySelector<HTMLElement>('[data-testid="organization-disable-error"]');
    expect(failure?.getAttribute('role')).toBe('alert');
    expect(failure?.textContent?.trim()).toBe('组织信息已被其他人修改，已为你刷新最新数据，请重试');
    expect(document.querySelectorAll('[data-testid="organization-disable-error"]')).toHaveLength(1);
  });

  it('reports the in-flight disable in the confirm label and disables both buttons', async () => {
    mountDialog({ saving: true });
    await nextTick();

    const dialog = alertdialog();
    const confirm = dialog.querySelector<HTMLButtonElement>('[data-testid="organization-disable-confirm"]');
    expect(confirm?.textContent?.trim()).toBe('停用中…');
    expect(confirm?.disabled).toBe(true);
    const cancel = Array.from(dialog.querySelectorAll('button')).find(
      (button) => button.textContent?.trim() === '取消',
    );
    expect(cancel?.disabled).toBe(true);
  });
});
