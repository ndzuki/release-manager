import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import type { AuditEvent } from '@/gen/audit/v1/audit_pb';
import AuditEventDetail from './AuditEventDetail.vue';
import AuditEventTable from './AuditEventTable.vue';
import AuditExportPanel from './AuditExportPanel.vue';

function auditEvent(id: string): AuditEvent {
  return {
    $typeName: 'audit.v1.AuditEvent' as const,
    id,
    actor: {
      $typeName: 'audit.v1.AuditActor' as const,
      kind: 2,
      // Server-side projection: a member would receive a masked id, a release
      // admin the full id. The frontend must render exactly what it is given
      // and never reconstruct or display role/displayName (AC-059-05, ADR-010).
      id: `user-${id}`,
      organizationId: 'org-1',
      role: 'release_admin',
    },
    resourceType: 'operation',
    resourceId: `operation-${id}`,
    action: 'upgrade',
    status: 'succeeded',
    durationMs: 10n,
    changeSummary: '',
    metadata: {},
  };
}

const fullIdEvent = auditEvent('abc123');
const maskedIdEvent = auditEvent('u***d3f');

describe('audit event components', () => {
  it('renders actor as kind:id without role or displayName in the table (AC-059-05)', () => {
    const wrapper = mount(AuditEventTable, {
      props: {
        events: [fullIdEvent, maskedIdEvent],
        totalSize: 2,
        loading: false,
        hasPrevious: false,
        hasMore: false,
      },
    });

    const text = wrapper.text();
    expect(text).toContain('user:user-abc123');
    expect(text).toContain('user:user-u***d3f');
    // Sensitive fields must never leak into the rendered DOM.
    expect(text).not.toContain('release_admin');
    expect(text).not.toContain('displayName');
  });

  it('renders only server-returned fields in the detail panel (AC-059-05)', () => {
    const wrapper = mount(AuditEventDetail, { props: { event: fullIdEvent } });

    const text = wrapper.text();
    expect(text).toContain('user:user-abc123');
    expect(text).toContain('operation:operation-abc123');
    expect(text).toContain('upgrade');
    expect(text).toContain('succeeded');
    // No role, no display name, no operation/request id columns — the wire
    // contract has none of these on AuditEvent, and role is redacted.
    expect(text).not.toContain('release_admin');
    expect(text).not.toContain('Operation ID');
    expect(text).not.toContain('Request ID');
  });

  it('emits row selection and pagination events', async () => {
    const wrapper = mount(AuditEventTable, {
      props: {
        events: [fullIdEvent],
        totalSize: 1,
        loading: false,
        hasPrevious: true,
        hasMore: true,
      },
    });

    await wrapper.get('tbody tr').trigger('click');
    expect(wrapper.emitted('select')).toHaveLength(1);
    expect(wrapper.emitted('select')![0]).toEqual([fullIdEvent]);

    await wrapper.get('[aria-label="Audit pagination"] button:last-child').trigger('click');
    expect(wrapper.emitted('next')).toHaveLength(1);
  });

  it('disables pagination buttons when no pages remain in either direction (AC-059-03)', () => {
    const wrapper = mount(AuditEventTable, {
      props: {
        events: [fullIdEvent],
        totalSize: 1,
        loading: false,
        hasPrevious: false,
        hasMore: false,
      },
    });

    const buttons = wrapper.get('[aria-label="Audit pagination"]').findAll('button');
    expect(buttons[0].attributes('disabled')).toBeDefined();
    expect(buttons[1].attributes('disabled')).toBeDefined();
  });

  it('shows export receipts with correlation id and status only (AC-059-04)', () => {
    const wrapper = mount(AuditExportPanel, {
      props: { tasks: [{ taskId: 'export-7', status: 'pending' }] },
    });

    const text = wrapper.text();
    expect(text).toContain('export-7');
    expect(text).toContain('pending');
    // Receipt only: no download link, no status polling button.
    expect(wrapper.find('a').exists()).toBe(false);
    expect(wrapper.find('button').exists()).toBe(false);
  });
});
