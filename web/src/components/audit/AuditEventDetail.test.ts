import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import { create } from '@bufbuild/protobuf';
import { TimestampSchema } from '@bufbuild/protobuf/wkt';
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

/*
 * W17 follow-up (review of 512b09f1): the detail panel is the forensic view of a
 * single event, and it lost the seconds the previous `toLocaleString()` call
 * printed. Two events in the same minute must not render identically here.
 */
describe('AuditEventDetail timestamp (W17 follow-up)', () => {
  it('renders the event time with seconds', () => {
    const at = new Date(2026, 9, 17, 20, 2, 46);
    const wrapper = mount(AuditEventDetail, {
      props: {
        event: {
          ...auditEvent(),
          createdAt: create(TimestampSchema, { seconds: BigInt(Math.floor(at.getTime() / 1000)) }),
        },
      },
    });

    expect(wrapper.text()).toMatch(/20:02:46/);
  });
});
