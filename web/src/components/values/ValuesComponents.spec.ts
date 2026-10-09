import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import SecretRefEditor from './SecretRefEditor.vue';
import ValuesDiffPanel from './ValuesDiffPanel.vue';
import ValuesRevisionActions from './ValuesRevisionActions.vue';
import type { ValuesRevision } from '@/types/valuesRevision';

const draft: ValuesRevision = {
  id: 'draft-1', releaseDefinitionId: 'definition-1', revision: 2, stateVersion: '1',
  document: '{"replicas":2}', valuesDigest: 'sha256:draft', status: 'pending_approval', parentRevisionId: 'parent-1',
  secretRefs: [], createdByUserId: 'creator-1', createdAt: '2026-07-23T00:00:00Z',
  convergenceTaskIds: [], lockedPaths: [],
};

// Submit/Save/Discard only render for a draft; `draft` above is pending_approval
// (used by the approval-action cases).
const editableDraft: ValuesRevision = { ...draft, status: 'draft' };

describe('values editor presentation', () => {
  it('explains when canonical diff has no changes', () => {
    const wrapper = mount(ValuesDiffPanel, { props: { result: { changes: [], hasChanges: false } } });
    expect(wrapper.text()).toContain('无 canonical 变化');
  });

  it('emits a complete SecretRef update with an automatically derived path', async () => {
    const wrapper = mount(SecretRefEditor, {
      props: {
        items: [{ id: 'ref-1', path: '', name: 'database', key: '' }],
        secrets: [{ name: 'database', keys: ['password'] }],
      },
    });

    await wrapper.findAll('select')[1].setValue('password');

    expect(wrapper.emitted('update')?.[0]).toEqual(['ref-1', { key: 'password', path: '.secrets.database.password' }]);
  });

  it('offers Save before any revision exists (creating the first one)', async () => {
    // The store's save() creates the revision when none exists yet (the first
    // configuration revision, or a prepared convergence session whose draft has
    // not been created). Without this button such a session is a dead end in the
    // UI — found by running web/e2e/emergency-smoke.spec.ts for real (TASK-270).
    const wrapper = mount(ValuesRevisionActions, {
      props: {
        revision: null, saving: false, approving: false, discarding: false, saveDisabled: false,
        submitDisabled: false,
        canApprove: false, selfApproval: false, readOnly: false,
      },
    });

    const save = wrapper.findAll('button').find((button) => button.text() === '保存 Draft');
    expect(save).toBeDefined();
    await save!.trigger('click');
    expect(wrapper.emitted('save')).toHaveLength(1);
    // Submit and Discard address a PERSISTED revision: they must stay absent.
    expect(wrapper.text()).not.toContain('提交');
    expect(wrapper.text()).not.toContain('丢弃');
  });

  it('hides every action in read-only mode', () => {
    const wrapper = mount(ValuesRevisionActions, {
      props: {
        revision: null, saving: false, approving: false, discarding: false, saveDisabled: false,
        submitDisabled: false,
        canApprove: false, selfApproval: false, readOnly: true,
      },
    });

    expect(wrapper.findAll('button')).toHaveLength(0);
  });

  it('does not render approval actions for self approval', () => {
    const wrapper = mount(ValuesRevisionActions, {
      props: {
        revision: draft, saving: false, approving: false, discarding: false, saveDisabled: false,
        submitDisabled: false,
        canApprove: false, selfApproval: true, readOnly: false,
      },
    });

    expect(wrapper.text()).toContain('不可审批自己创建的 Revision');
    expect(wrapper.text()).not.toContain('审批通过');
    expect(wrapper.text()).not.toContain('驳回');
  });

  // UX-003: submit approves the PERSISTED revision, so the button must be off
  // while the editor holds content that has not been saved (or is invalid).
  it('disables Submit and explains why when the draft has unsaved changes', () => {
    const wrapper = mount(ValuesRevisionActions, {
      props: {
        revision: editableDraft, saving: false, approving: false, discarding: false,
        saveDisabled: false, submitDisabled: true,
        canApprove: false, selfApproval: false, readOnly: false,
      },
    });

    const submit = wrapper.findAll('button').find((button) => button.text() === '提交')!;
    expect(submit.attributes('disabled')).toBeDefined();
    expect(submit.attributes('title')).toContain('保存');
    expect(wrapper.emitted('submit')).toBeUndefined();
  });

  it('enables Submit once the draft is saved and valid', async () => {
    const wrapper = mount(ValuesRevisionActions, {
      props: {
        revision: editableDraft, saving: false, approving: false, discarding: false,
        saveDisabled: false, submitDisabled: false,
        canApprove: false, selfApproval: false, readOnly: false,
      },
    });

    const submit = wrapper.findAll('button').find((button) => button.text() === '提交')!;
    expect(submit.attributes('disabled')).toBeUndefined();
    await submit.trigger('click');
    expect(wrapper.emitted('submit')).toHaveLength(1);
  });

  it('renders approve and reject only for an eligible draft', () => {
    const wrapper = mount(ValuesRevisionActions, {
      props: {
        revision: draft, saving: false, approving: false, discarding: false, saveDisabled: false,
        submitDisabled: false,
        canApprove: true, selfApproval: false, readOnly: false,
      },
    });

    expect(wrapper.text()).toContain('审批通过');
    expect(wrapper.text()).toContain('驳回');
  });
  it('renders rejected status with its decision timestamp', () => {
    const wrapper = mount(ValuesRevisionActions, {
      props: {
        revision: {
          ...draft,
          status: 'rejected',
          decidedAt: '2026-07-23T01:00:00Z',
        },
        saving: false,
        approving: false,
        discarding: false,
        saveDisabled: true,
        submitDisabled: true,
        canApprove: false,
        selfApproval: false,
        readOnly: false,
      },
    });

    expect(wrapper.text()).toContain('驳回');
    expect(wrapper.text()).toContain('2026');
  });

});
