import { mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import TrustRootEditor from './TrustRootEditor.vue';
import type { TrustRootView } from '@/connect/trust-api';

/*
 * TASK-294. The panel renders through the shared AppDialog, which teleports to <body>
 * (ADR-029), so the dialog is queried in the document rather than through the wrapper —
 * the pattern AppDialog.test.ts uses for teleported overlays.
 */
const mounted: Array<{ unmount: () => void }> = [];

afterEach(() => {
  // Unmount before wiping the DOM: the focus trap keeps a module-level stack of open
  // overlays, and a component that is never unmounted leaves a stale entry behind that
  // would make every later Escape/Tab look broken.
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
});

beforeEach(() => {
  setActivePinia(createPinia());
});

/** The unique dialog panel, whether it is a `dialog` or a destructive `alertdialog`. */
function dialogPanel(): HTMLElement {
  const matches = Array.from(document.querySelectorAll<HTMLElement>('[role="dialog"], [role="alertdialog"]'));
  if (matches.length !== 1) throw new Error(`expected exactly one dialog role, found ${matches.length}`);
  return matches[0]!;
}

/** The accessible name AppDialog gives its panel: aria-labelledby -> the heading. */
function accessibleName(element: HTMLElement): string {
  const id = element.getAttribute('aria-labelledby');
  const labelled = id ? document.getElementById(id) : null;
  return (labelled?.textContent ?? element.getAttribute('aria-label') ?? '').trim();
}

function rootView(keyId: string): TrustRootView {
  return {
    id: `root-${keyId}`,
    keyId,
    issuer: 'CN=dev',
    subjectPattern: 'release-manager',
    publicKeyPem: 'pem',
    state: 'active',
    validFrom: '2026-09-28T00:00:00Z',
    graceUntil: null,
    createdAt: null,
    updatedAt: null,
    revokedAt: null,
  };
}

function mountEditor(props: { mode: 'create' | 'rotate'; root?: TrustRootView | null } = { mode: 'create' }) {
  // A focused trigger in the document: focus return only resolves for attached elements.
  const trigger = document.createElement('button');
  trigger.textContent = '新建信任根';
  document.body.append(trigger);
  trigger.focus();

  const wrapper = mount(TrustRootEditor, { attachTo: document.body, props });
  mounted.push(wrapper);
  return wrapper;
}

describe('TrustRootEditor dialog semantics', () => {
  it('renders as a labelled modal dialog instead of a bare inline form', () => {
    const wrapper = mountEditor();

    const panel = dialogPanel();
    expect(panel.getAttribute('role')).toBe('dialog'); // create/rotate are reversible
    expect(panel.getAttribute('aria-modal')).toBe('true');
    expect(accessibleName(panel)).toBe('新建信任根');
    expect(panel.querySelector('form.trust-editor')).not.toBeNull();
    // It really left the component tree: a bare inline panel would still be inside it.
    expect(wrapper.find('form.trust-editor').exists()).toBe(false);
  });

  it('names the panel after the root it rotates', () => {
    mountEditor({ mode: 'rotate', root: rootView('dev-trust-root') });

    expect(accessibleName(dialogPanel())).toBe('轮换信任根 dev-trust-root');
    expect(document.querySelector('[data-testid="trust-editor-grace-hours"]')).not.toBeNull();
  });

  it('closes on Escape and reports it through the close event', async () => {
    const wrapper = mountEditor();
    await wrapper.vm.$nextTick();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));

    expect(wrapper.emitted('close')).toHaveLength(1);
  });
});
