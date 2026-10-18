import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import { nextTick } from 'vue';
import type { DefinitionCreateForm } from '@/composables/useDefinitionCreate';
import DefinitionCreateDialog from './DefinitionCreateDialog.vue';

/*
 * TASK-363: the create dialog was carved out of DefinitionsPage. The page owns the
 * form; this component owns the markup, the per-instance label ids and the intent
 * events. The two failure slots must render INSIDE the dialog: the page-level
 * <ErrorState> is behind the fixed backdrop.
 */

const mounted: Array<{ unmount: () => void }> = [];

afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
});

function blankForm(): DefinitionCreateForm {
  return { customerId: '', clusterId: '', namespace: '', releaseName: '', chartName: '', enabled: true };
}

function mountCreate(overrides: {
  form?: DefinitionCreateForm;
  saving?: boolean;
  validationError?: string;
  failure?: string | null;
} = {}) {
  const wrapper = mount(DefinitionCreateDialog, {
    props: {
      open: true,
      saving: overrides.saving ?? false,
      validationError: overrides.validationError ?? '',
      failure: overrides.failure ?? null,
      form: overrides.form ?? blankForm(),
    },
  });
  mounted.push(wrapper);
  return wrapper;
}

function button(root: ParentNode, label: string): HTMLButtonElement {
  const element = Array.from(root.querySelectorAll<HTMLButtonElement>('button')).find(
    (candidate) => candidate.textContent?.trim() === label,
  );
  if (!element) throw new Error(`button not found: ${label}`);
  return element;
}

describe('DefinitionCreateDialog', () => {
  it('renders the five fields with their own labels and the enabled checkbox', () => {
    mountCreate();

    const inputs = Array.from(document.querySelectorAll<HTMLInputElement>('.definitions__field input'));
    expect(inputs).toHaveLength(5);
    for (const input of inputs) {
      const id = input.getAttribute('id');
      expect(id).toBeTruthy();
      const label = document.querySelector(`.definitions__field label[for="${id}"]`);
      expect(label?.textContent?.trim()).not.toBe('');
    }
    expect(new Set(inputs.map((input) => input.getAttribute('id'))).size).toBe(5);
    expect(document.querySelector<HTMLInputElement>('input[name="create-enabled"]')?.checked).toBe(true);
  });

  it('edits the form it was handed in place (the page owns it)', async () => {
    const form = blankForm();
    mountCreate({ form });

    const input = document.querySelector<HTMLInputElement>('input[name="create-customerId"]')!;
    input.value = 'cust-1';
    input.dispatchEvent(new Event('input'));
    await nextTick();
    document.querySelector<HTMLInputElement>('input[name="create-enabled"]')!.click();
    await nextTick();

    expect(form.customerId).toBe('cust-1');
    expect(form.enabled).toBe(false);
  });

  it('renders both failure slots as alerts inside the dialog', () => {
    document.body.innerHTML = '';
    const wrapper = mount(DefinitionCreateDialog, {
      props: {
        open: true,
        saving: false,
        validationError: '请填写客户 ID、集群 ID 与 Release 名称',
        failure: '相同来源与目标的定义已存在',
        form: blankForm(),
      },
    });
    mounted.push(wrapper);

    const validation = document.querySelector<HTMLElement>('[data-testid="definitions-create-validation"]');
    const failure = document.querySelector<HTMLElement>('[data-testid="definitions-create-error"]');
    expect(validation?.textContent?.trim()).toBe('请填写客户 ID、集群 ID 与 Release 名称');
    expect(validation?.getAttribute('role')).toBe('alert');
    expect(failure?.textContent?.trim()).toBe('相同来源与目标的定义已存在');
    expect(failure?.getAttribute('role')).toBe('alert');
    expect(document.querySelector('.app-dialog__body')?.contains(failure ?? null)).toBe(true);
  });

  it('reports submit and close as events', async () => {
    const wrapper = mountCreate();

    document.querySelector<HTMLFormElement>('.definitions__create form')!.dispatchEvent(new Event('submit'));
    await nextTick();
    expect(wrapper.emitted('submit')).toHaveLength(1);

    button(document, '取消').click();
    await nextTick();
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('disables both actions while a create is in flight', () => {
    mountCreate({ saving: true });

    expect(button(document, '取消').disabled).toBe(true);
    expect(document.querySelector<HTMLButtonElement>('[data-testid="definitions-create-submit"]')?.disabled).toBe(true);
  });
});
