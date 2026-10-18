import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import type { AuditEvent } from '@/gen/audit/v1/audit_pb';
import AuditEventDetail from './AuditEventDetail.vue';

/*
 * UX-019 (W14): the detail panel replaces the list while open but ignored
 * Escape — the only way out was the close button, and the panel is opened from
 * a row that keeps focus, so a keyboard user could not dismiss it in place.
 */
function auditEvent(): AuditEvent {
  return {
    $typeName: 'audit.v1.AuditEvent' as const,
    id: 'e-1',
    actor: undefined,
    resourceType: 'operation',
    resourceId: 'op-1',
    action: 'upgrade',
    status: 'succeeded',
    durationMs: 10n,
    changeSummary: '',
    metadata: {},
  } as unknown as AuditEvent;
}

describe('AuditEventDetail Escape handling (UX-019)', () => {
  it('closes on Escape and ignores every other key', async () => {
    const wrapper = mount(AuditEventDetail, { props: { event: auditEvent() }, attachTo: document.body });

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'a' }));
    expect(wrapper.emitted('close')).toBeUndefined();

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(wrapper.emitted('close')).toHaveLength(1);

    wrapper.unmount();
  });

  it('stops listening once unmounted', () => {
    const wrapper = mount(AuditEventDetail, { props: { event: auditEvent() }, attachTo: document.body });
    wrapper.unmount();
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(wrapper.emitted('close')).toBeUndefined();
  });
});
