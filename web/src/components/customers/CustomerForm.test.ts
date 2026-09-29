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
