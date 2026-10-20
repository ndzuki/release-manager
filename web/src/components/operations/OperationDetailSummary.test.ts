import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import { statusLabel } from '@/i18n/status-labels';
import { formatTimestamp } from '@/utils/formatTimestamp';
import type { Operation } from '@/types/operation';
import OperationDetailSummary from './OperationDetailSummary.vue';

/*
 * The summary block carries the authoritative fields the timeline does not repeat.
 * W17 pinned one timestamp implementation for the console; the detail page is one
 * of the call sites that needs second precision, so the style is part of the
 * contract asserted here (not just that "a time" rendered).
 */

function operation(overrides: Partial<Operation> = {}): Operation {
  return {
    operationId: 'op-1',
    releaseDefinitionId: 'def-1',
    operationType: 'INSTALL',
    state: 'running',
    stateVersion: 7n,
    bundleId: '',
    valuesRevisionId: '',
    expectedRevision: 0,
    targetRevision: 3,
    createdBy: 'user-1',
    createdAt: '2026-10-01T00:00:00.000Z',
    updatedAt: '2026-10-01T00:05:00.000Z',
    terminalAt: null,
    deadline: null,
    lastError: '',
    effectStatus: 'NOT_STARTED',
    reasonDisplay: '',
    ...overrides,
  };
}

describe('OperationDetailSummary', () => {
  it('renders the authoritative fields', () => {
    const wrapper = mount(OperationDetailSummary, { props: { operation: operation() } });

    const text = wrapper.text();
    expect(text).toContain('发布定义');
    expect(text).toContain('def-1');
    expect(text).toContain('状态版本');
    expect(text).toContain('7');
    expect(text).toContain('目标版本');
    expect(text).toContain('3');
  });

  it('keeps second precision on the detail timestamps (medium style)', () => {
    const value = operation();
    const wrapper = mount(OperationDetailSummary, { props: { operation: value } });

    const rendered = wrapper.findAll('dd').map((cell) => cell.text());
    expect(rendered).toContain(formatTimestamp(value.createdAt, { style: 'medium' }));
    expect(rendered).toContain(formatTimestamp(value.updatedAt, { style: 'medium' }));
    expect(rendered.join(' ')).toMatch(/\d{2}:\d{2}:\d{2}/);
  });

  it('renders an em dash for an absent revision and terminal time', () => {
    const wrapper = mount(OperationDetailSummary, {
      props: { operation: operation({ targetRevision: 0, terminalAt: null }) },
    });

    const cells = wrapper.findAll('dd').map((cell) => cell.text());
    expect(cells).toContain('—');
    expect(cells.filter((cell) => cell === '—')).toHaveLength(2);
  });

  it('shows the effect status row only for EMERGENCY operations', () => {
    const plain = mount(OperationDetailSummary, { props: { operation: operation() } });
    expect(plain.text()).not.toContain(statusLabel('effect', 'APPLIED'));

    const emergency = mount(OperationDetailSummary, {
      props: { operation: operation({ operationType: 'EMERGENCY', effectStatus: 'APPLIED' }) },
    });
    expect(emergency.text()).toContain(statusLabel('effect', 'APPLIED'));
  });

  // AC-058-48 / TASK-325 W3: the page renders only the server's safe
  // projection. A missing reason renders no row at all, so ordinary
  // operations keep their existing summary shape.
  it('renders the reason projection only when the operation carries one', () => {
    const withoutReason = mount(OperationDetailSummary, { props: { operation: operation() } });
    expect(withoutReason.text()).not.toContain('变更原因');

    const withReason = mount(OperationDetailSummary, {
      props: {
        operation: operation({
          operationType: 'EMERGENCY',
          effectStatus: 'APPLIED',
          reasonDisplay: 'incident-42 rotate password=****REDACTED****',
        }),
      },
    });
    expect(withReason.text()).toContain('变更原因');
    expect(withReason.text()).toContain('incident-42 rotate password=****REDACTED****');
    expect(withReason.findAll('.operation-detail__summary-reason')).toHaveLength(1);
  });
});
