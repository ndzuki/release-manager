import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import { nextTick } from 'vue';
import OrganizationCreateDialog from './OrganizationCreateDialog.vue';

/*
 * TASK-363: the create-organization dialog was carved out of OrganizationPage.
 * AppDialog teleports to <body> (ADR-029), so the controls are queried in the
 * document — the pattern the page tests use for teleported overlays. The label/id
 * closure is pinned here because useId() moved into this component with the form.
 */

const mounted: Array<{ unmount: () => void }> = [];

afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
});

function mountDialog(
  overrides: {
    open?: boolean;
    name?: string;
    saving?: boolean;
    validationError?: string;
    failure?: string | null;
  } = {},
) {
  const wrapper = mount(OrganizationCreateDialog, {
    props: {
      open: overrides.open ?? true,
      name: overrides.name ?? '',
      saving: overrides.saving ?? false,
      validationError: overrides.validationError ?? '',
      failure: overrides.failure ?? null,
    },
    attachTo: document.body,
  });
  mounted.push(wrapper);
  return wrapper;
}

function dialogRoot(): HTMLElement {
  const root = document.querySelector<HTMLElement>('.organization__create');
  if (!root) throw new Error('organization create dialog not rendered');
  return root;
}

describe('OrganizationCreateDialog', () => {
  it('associates the name field with its label', async () => {
    mountDialog();
    await nextTick();

    const input = dialogRoot().querySelector<HTMLInputElement>('input[name="organizationName"]');
    expect(input).not.toBeNull();
    const id = input?.getAttribute('id');
    expect(id).toBeTruthy();
    const label = dialogRoot().querySelector(`.organization__field label[for="${id}"]`);
    expect(label).not.toBeNull();
    expect(label?.textContent?.trim()).toBe('组织名称');
  });

  it('emits submit from the form and close from the cancel button', async () => {
    const wrapper = mountDialog();
    await nextTick();

    dialogRoot().querySelector('form')?.dispatchEvent(new Event('submit'));
    const cancel = Array.from(dialogRoot().querySelectorAll('button')).find(
      (button) => button.textContent?.trim() === '取消',
    );
    cancel?.click();

    expect(wrapper.emitted('submit')).toHaveLength(1);
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('reports the name model update', async () => {
    const wrapper = mountDialog();
    await nextTick();

    const input = dialogRoot().querySelector<HTMLInputElement>('input[name="organizationName"]');
    if (!input) throw new Error('name input not rendered');
    input.value = 'Acme';
    input.dispatchEvent(new Event('input'));

    expect(wrapper.emitted('update:name')?.[0]).toEqual(['Acme']);
  });

  it('renders the validation and the refusal inside the dialog', async () => {
    mountDialog({ validationError: '组织名称', failure: 'requires role platform_admin' });
    await nextTick();

    const validation = document.querySelector('[data-testid="organization-create-validation"]');
    const failure = document.querySelector('[data-testid="organization-create-error"]');
    expect(validation?.textContent?.trim()).toBe('组织名称');
    expect(validation?.getAttribute('role')).toBe('alert');
    expect(failure?.textContent?.trim()).toBe('requires role platform_admin');
  });

  it('reports the in-flight create in the submit label and blocks close', async () => {
    mountDialog({ saving: true });
    await nextTick();

    const submit = document.querySelector<HTMLButtonElement>('[data-testid="organization-create-submit"]');
    expect(submit?.textContent?.trim()).toBe('创建中…');
    expect(submit?.disabled).toBe(true);
    expect(document.querySelector('[role="dialog"]')?.getAttribute('aria-modal')).toBe('true');
  });

  it('renders nothing while closed', async () => {
    mountDialog({ open: false });
    await nextTick();

    expect(document.querySelector('.organization__create')).toBeNull();
  });
});
