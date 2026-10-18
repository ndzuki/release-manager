import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import CapabilityGrantForm from './CapabilityGrantForm.vue';
import { AUTHORIZATION_ACTIONS, type AuthorizationAction, type GrantVersions } from '@/connect/binding-api';

/*
 * TASK-363: the capability-grant form, pinned as a presentational contract.
 * The page owns the state and the submission; the form owns the markup and the
 * three models plus the server-answered versions (there is no list-grants RPC).
 */
function mountForm(
  props: {
    subject?: string;
    action?: AuthorizationAction;
    desiredState?: 'grant' | 'revoke';
    saving?: boolean;
    error?: string;
    versions?: GrantVersions | null;
  } = {},
) {
  return mount(CapabilityGrantForm, {
    props: {
      subject: '',
      action: 'release.emergency.resolve',
      desiredState: 'grant',
      saving: false,
      error: '',
      versions: null,
      ...props,
    },
  });
}

describe('CapabilityGrantForm', () => {
  it('renders the original panel markup and every authorization action', () => {
    const wrapper = mountForm();

    expect(wrapper.get('form').classes()).toContain('governance__panel');
    expect(wrapper.get('h2').text()).toBe('能力授予');
    expect(wrapper.get('.governance__hint').text()).toContain('没有「列出既有授权」的 RPC');
    expect(wrapper.get('select[name="action"]').findAll('option').map((option) => option.text())).toEqual([
      ...AUTHORIZATION_ACTIONS,
    ]);
    expect(wrapper.get('select[name="desiredState"]').findAll('option').map((option) => option.text())).toEqual([
      '授予',
      '撤销',
    ]);
  });

  it('emits submit and reports the three models', async () => {
    const wrapper = mountForm();

    await wrapper.get('input[name="subject"]').setValue('user-9');
    await wrapper.get('select[name="action"]').setValue('release.values.approve');
    await wrapper.get('select[name="desiredState"]').setValue('revoke');
    await wrapper.get('form').trigger('submit');

    expect(wrapper.emitted('update:subject')?.at(-1)).toEqual(['user-9']);
    expect(wrapper.emitted('update:action')?.at(-1)).toEqual(['release.values.approve']);
    expect(wrapper.emitted('update:desiredState')?.at(-1)).toEqual(['revoke']);
    expect(wrapper.emitted('submit')).toHaveLength(1);
  });

  it('shows the versions the server answered', () => {
    const wrapper = mountForm({ versions: { sourceVersion: 3n, policyVersion: 4n } });

    expect(wrapper.get('[data-testid="grant-versions"]').text()).toBe('来源 / 策略 3 / 4');
  });

  it('shows the validation error with its testid and alert role', () => {
    const wrapper = mountForm({ error: '请填写主体（用户 ID）' });

    const error = wrapper.get('[data-testid="grant-error"]');
    expect(error.text()).toBe('请填写主体（用户 ID）');
    expect(error.attributes('role')).toBe('alert');
  });

  it('disables the submit and shows the in-flight label while saving', () => {
    const wrapper = mountForm({ saving: true });

    const submit = wrapper.get('button[type="submit"]');
    expect(submit.text()).toBe('提交中…');
    expect((submit.element as HTMLButtonElement).disabled).toBe(true);
  });
});
