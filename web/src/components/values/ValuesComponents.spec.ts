import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import SecretRefEditor from './SecretRefEditor.vue';
import ValuesDiffPanel from './ValuesDiffPanel.vue';
import ValuesRevisionActions from './ValuesRevisionActions.vue';
import type { ValuesRevision } from '@/types/valuesRevision';

const draft: ValuesRevision = {
  id: 'draft-1', releaseDefinitionId: 'definition-1', revision: 2, version: 1,
  document: '{"replicas":2}', valuesDigest: 'sha256:draft', status: 'draft', parentRevisionId: 'parent-1',
  secretRefs: [], createdBy: 'creator-1', createdAt: '2026-07-23T00:00:00Z',
};

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

  it('does not render approval actions for self approval', () => {
    const wrapper = mount(ValuesRevisionActions, {
      props: {
        revision: draft, saving: false, approving: false, saveDisabled: false,
        canApprove: false, selfApproval: true, readOnly: false,
      },
    });

    expect(wrapper.text()).toContain('不可审批自己创建的 Revision');
    expect(wrapper.text()).not.toContain('Approve');
    expect(wrapper.text()).not.toContain('Reject');
  });

  it('renders approve and reject only for an eligible draft', () => {
    const wrapper = mount(ValuesRevisionActions, {
      props: {
        revision: draft, saving: false, approving: false, saveDisabled: false,
        canApprove: true, selfApproval: false, readOnly: false,
      },
    });

    expect(wrapper.text()).toContain('Approve');
    expect(wrapper.text()).toContain('Reject');
  });
  it('renders rejected status with its reason and timestamp', () => {
    const wrapper = mount(ValuesRevisionActions, {
      props: {
        revision: {
          ...draft,
          status: 'rejected',
          reason: 'increase the replica count',
          rejectedAt: '2026-07-23T01:00:00Z',
        },
        saving: false,
        approving: false,
        saveDisabled: true,
        canApprove: false,
        selfApproval: false,
        readOnly: false,
      },
    });

    expect(wrapper.text()).toContain('Rejected');
    expect(wrapper.text()).toContain('increase the replica count');
    expect(wrapper.text()).toContain('2026');
  });

});
