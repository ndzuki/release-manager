import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import CustomerForm from './CustomerForm.vue';

/*
 * The customer form's branches had no coverage (an independent review pointed this
 * out), even though the copy migration touched all of them.
 */
function mountForm(props: Record<string, unknown> = {}) {
  return mount(CustomerForm, {
    props: {
      modelValue: { name: 'Acme', slug: 'acme', version: 1 },
      submitting: false,
      readonly: false,
      fieldViolations: [],
      submitLabel: '保存客户',
      ...props,
    },
  });
}

describe('CustomerForm', () => {
  it('renders its labels and submit label from the catalog', () => {
    const wrapper = mountForm();

    expect(wrapper.text()).toContain('名称');
    expect(wrapper.text()).toContain('标识');
    expect(wrapper.text()).toContain('保存客户');
  });

  it('explains read-only access instead of offering an editable field', () => {
    const wrapper = mountForm({ readonly: true });

    expect(wrapper.text()).toContain('只读访问');
  });

  it('shows the saving label while submitting', () => {
    const wrapper = mountForm({ submitting: true });

    expect(wrapper.text()).toContain('保存中');
  });

  it('surfaces field-level errors', () => {
    const wrapper = mountForm({
      fieldViolations: [
        { field: 'name', description: '名称必填' },
        { field: 'slug', description: '标识格式不合法' },
      ],
    });

    expect(wrapper.text()).toContain('名称必填');
    expect(wrapper.text()).toContain('标识格式不合法');
  });
});

/*
 * TASK-271 (A11y subset ②): a field error must be a DESCRIPTION of its control, never
 * part of its accessible NAME. The name is what `label[for]` contributes, and the error
 * used to sit inside the wrapping label, so pointing aria-describedby at the same node
 * would have made a screen reader say the sentence twice. The two halves are asserted
 * separately for BOTH fields, so removing either relation fails here.
 */
describe('CustomerForm field errors are described, not named', () => {
  const cases = [
    { field: 'name', description: '名称必填', inputIndex: 0 },
    { field: 'slug', description: '标识格式不合法', inputIndex: 1 },
  ] as const;

  it.each(cases)('describes the $field control with its alert', ({ field, description, inputIndex }) => {
    const wrapper = mountForm({ fieldViolations: [{ field, description }] });
    const input = wrapper.findAll('input')[inputIndex]!;
    const id = input.attributes('id');
    const describedBy = input.attributes('aria-describedby');
    const label = wrapper.get(`label[for="${id}"]`);

    // ① the accessible name is the label text only — the error is not in it ...
    expect(label.attributes('for')).toBe(id);
    expect(label.text()).not.toContain(description);
    expect(label.find('.form-field__error').exists()).toBe(false);
    // ② ... it is reachable as the control's description instead.
    expect(describedBy).toBeTruthy();
    expect(wrapper.get(`#${describedBy}`).text()).toContain(description);
    expect(wrapper.get(`#${describedBy}`).attributes('role')).toBe('alert');
    expect(input.attributes('aria-invalid')).toBe('true');
  });
});
