import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import BindingCreateForm from './BindingCreateForm.vue';

/*
 * TASK-363: the new-binding form, pinned as a presentational contract.
 * The page owns the state and the submission; the form owns the markup and the
 * `customerId` / `error` / `saving` projections.
 */
function mountForm(props: { customerId?: string; saving?: boolean; error?: string } = {}) {
  return mount(BindingCreateForm, {
    props: { customerId: '', saving: false, error: '', ...props },
  });
}

describe('BindingCreateForm', () => {
  it('renders the original panel markup and emits submit', async () => {
    const wrapper = mountForm();

    expect(wrapper.get('form').classes()).toContain('governance__panel');
    expect(wrapper.get('h2').text()).toBe('新增绑定');
    expect(wrapper.get('label').text()).toContain('客户 ID');
    expect(wrapper.get('input[name="customerId"]').attributes('placeholder')).toBe('客户 ID');

    await wrapper.get('form').trigger('submit');

    expect(wrapper.emitted('submit')).toHaveLength(1);
  });

  it('reports the typed customer id through the model', async () => {
    const wrapper = mountForm();

    await wrapper.get('input[name="customerId"]').setValue('customer-c');

    expect(wrapper.emitted('update:customerId')?.at(-1)).toEqual(['customer-c']);
  });

  it('shows the validation error with its testid and alert role', () => {
    const wrapper = mountForm({ error: '请填写客户 ID' });

    const error = wrapper.get('[data-testid="binding-create-error"]');
    expect(error.text()).toBe('请填写客户 ID');
    expect(error.attributes('role')).toBe('alert');
  });

  it('disables the submit and shows the in-flight label while saving', () => {
    const wrapper = mountForm({ saving: true });

    const submit = wrapper.get('button[type="submit"]');
    expect(submit.text()).toBe('提交中…');
    expect((submit.element as HTMLButtonElement).disabled).toBe(true);
  });
});
