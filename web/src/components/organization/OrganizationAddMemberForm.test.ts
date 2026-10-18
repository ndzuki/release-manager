import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import type { OrganizationRole } from '@/connect/organization-api';
import OrganizationAddMemberForm from './OrganizationAddMemberForm.vue';

/*
 * TASK-363: the add-member form was carved out of OrganizationPage. The two v-model
 * contracts and the submit event are the page's only coupling to it, so they are
 * pinned here: a dropped event would silently break adding a member.
 */

function mountForm(
  overrides: {
    userId?: string;
    role?: OrganizationRole;
    roles?: OrganizationRole[];
    saving?: boolean;
    error?: string;
  } = {},
) {
  return mount(OrganizationAddMemberForm, {
    props: {
      userId: overrides.userId ?? '',
      role: overrides.role ?? 'viewer',
      roles: overrides.roles ?? ['release_admin', 'deployer', 'viewer'],
      saving: overrides.saving ?? false,
      error: overrides.error ?? '',
    },
  });
}

describe('OrganizationAddMemberForm', () => {
  it('renders the form and only the grantable roles', () => {
    const wrapper = mountForm();

    expect(wrapper.get('h2').text()).toBe('添加成员');
    expect(wrapper.get('input[name="userId"]').attributes('placeholder')).toBe('用户 ID');
    expect(wrapper.findAll('option').map((option) => option.element.value)).toEqual([
      'release_admin',
      'deployer',
      'viewer',
    ]);
    expect(wrapper.get('button').text()).toBe('添加');
  });

  it('reports a submit from the form, not from the button markup', async () => {
    const wrapper = mountForm();

    await wrapper.get('form').trigger('submit');

    expect(wrapper.emitted('submit')).toHaveLength(1);
  });

  it('reports the two model updates', async () => {
    const wrapper = mountForm();

    await wrapper.get('input[name="userId"]').setValue('new-user');
    await wrapper.get('select[name="role"]').setValue('deployer');

    expect(wrapper.emitted('update:userId')?.[0]).toEqual(['new-user']);
    expect(wrapper.emitted('update:role')?.[0]).toEqual(['deployer']);
  });

  it('renders a local error as an alert inside the form', () => {
    const wrapper = mountForm({ error: '请填写用户 ID' });

    const error = wrapper.get('[data-testid="organization-add-error"]');
    expect(error.attributes('role')).toBe('alert');
    expect(error.text()).toBe('请填写用户 ID');
  });

  it('reports the in-flight write in the button label and disables it', () => {
    const wrapper = mountForm({ saving: true });

    const button = wrapper.get<HTMLButtonElement>('button');
    expect(button.text()).toBe('提交中…');
    expect(button.element.disabled).toBe(true);
  });
});
