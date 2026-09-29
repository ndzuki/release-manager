import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import { h, nextTick } from 'vue';
import AppDialog from './AppDialog.vue';

/*
 * The rendered dialog is teleported to `document.body`, so these tests query the
 * document rather than the wrapper (Vue Test Utils scopes `wrapper.find()` to the
 * component's own tree). That is deliberate: the first test asserts the content
 * really left the wrapper, which is what makes the overlay escape transformed
 * ancestors and stacking contexts.
 */
const mounted: { unmount: () => void }[] = [];

function mountDialog(props: Record<string, unknown> = {}) {
  const wrapper = mount(AppDialog, {
    attachTo: document.body,
    props: { open: true, title: 'Revoke operator-1', ...props },
    slots: {
      default: () => h('button', { class: 'inside-first' }, 'Inside'),
      footer: () => h('button', { class: 'inside-last' }, 'Confirm'),
    },
  });
  mounted.push(wrapper);
  return wrapper;
}

function panelElement(): HTMLElement {
  const element = document.querySelector<HTMLElement>('.app-dialog__panel');
  if (!element) throw new Error('dialog panel not rendered');
  return element;
}

function backdrop(): HTMLElement {
  const element = document.querySelector<HTMLElement>('.app-dialog');
  if (!element) throw new Error('dialog backdrop not rendered');
  return element;
}

afterEach(() => {
  // Unmount, don't just wipe the DOM: the focus trap keeps a module-level stack
  // of open overlays, and a component that is never unmounted leaves a stale
  // entry behind that makes every later test's Tab/Escape look broken.
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
});

describe('AppDialog', () => {
  it('teleports the overlay out of the component tree and labels it', () => {
    const wrapper = mountDialog({ description: 'This cannot be undone.' });

    // Out of the wrapper, into body.
    expect(wrapper.find('.app-dialog__panel').exists()).toBe(false);
    const dialogPanel = panelElement();
    expect(document.body.contains(dialogPanel)).toBe(true);

    expect(dialogPanel.getAttribute('role')).toBe('dialog');
    expect(dialogPanel.getAttribute('aria-modal')).toBe('true');

    const titleId = dialogPanel.getAttribute('aria-labelledby')!;
    expect(titleId).toBeTruthy();
    expect(document.getElementById(titleId)?.textContent).toBe('Revoke operator-1');

    const descriptionId = dialogPanel.getAttribute('aria-describedby')!;
    expect(document.getElementById(descriptionId)?.textContent).toBe('This cannot be undone.');
  });

  it('uses alertdialog and omits aria-describedby when there is no description', () => {
    mountDialog({ danger: true });

    const dialogPanel = panelElement();
    expect(dialogPanel.getAttribute('role')).toBe('alertdialog');
    expect(dialogPanel.getAttribute('aria-describedby')).toBeNull();
  });

  it('moves focus inside when it opens and returns it to the trigger when it closes', async () => {
    const trigger = document.createElement('button');
    trigger.textContent = 'Open dialog';
    document.body.append(trigger);
    trigger.focus();
    expect(document.activeElement).toBe(trigger);

    const wrapper = mountDialog();
    await wrapper.vm.$nextTick();
    expect(document.activeElement?.classList.contains('inside-first')).toBe(true);

    await wrapper.setProps({ open: false });
    await wrapper.vm.$nextTick();
    expect(document.activeElement).toBe(trigger);
  });

  it('closes on Escape', () => {
    const wrapper = mountDialog();

    backdrop().dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));

    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('closes on a backdrop click by default', () => {
    const wrapper = mountDialog();

    backdrop().click();

    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('does not close while escape and backdrop dismissal are disabled', async () => {
    const wrapper = mountDialog({ closeOnEscape: false, closeOnBackdrop: false });

    backdrop().dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    backdrop().click();

    expect(wrapper.emitted('close')).toBeUndefined();
  });

  it('closes on Escape even when focus sits outside the panel', () => {
    const wrapper = mountDialog();
    const outside = document.createElement('button');
    document.body.append(outside);
    outside.focus();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));

    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('traps Tab and handles Escape when it is mounted closed and opened later', async () => {
    // The production shape for DisableCustomerDialog: always mounted, `open`
    // toggled by the page. Registering the trap only on the mount tick left this
    // path with no Tab containment and no Escape at all.
    const wrapper = mountDialog({ open: false });
    await wrapper.setProps({ open: true });
    await nextTick();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(wrapper.emitted('close')).toHaveLength(1);

    const panel = panelElement();
    const last = panel.querySelector<HTMLElement>('.inside-last')!;
    last.focus();
    last.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }));
    expect(document.activeElement?.classList.contains('inside-first')).toBe(true);
  });

  it('lets only the topmost dialog react to Escape and Tab', () => {
    const lower = mountDialog({ title: 'Lower' });
    const upper = mountDialog({ title: 'Upper' });

    // Two document-level traps are registered; only the last one may act, or the
    // lower dialog pulls focus behind the top one.
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(upper.emitted('close')).toHaveLength(1);
    expect(lower.emitted('close')).toBeUndefined();

    const panels = Array.from(document.querySelectorAll<HTMLElement>('.app-dialog__panel'));
    const top = panels[panels.length - 1]!;
    const buttons = Array.from(top.querySelectorAll<HTMLElement>('button'));
    const lastButton = buttons[buttons.length - 1]!;
    lastButton.focus();
    lastButton.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }));
    expect(top.contains(document.activeElement)).toBe(true);
  });

  it('keeps Tab inside the dialog (wraps last → first and first → last)', async () => {
    mountDialog();
    const first = document.querySelector<HTMLElement>('.inside-first')!;
    const last = document.querySelector<HTMLElement>('.inside-last')!;

    last.focus();
    last.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }));
    expect(document.activeElement).toBe(first);

    first.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true }));
    expect(document.activeElement).toBe(last);
  });
});
