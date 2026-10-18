import { describe, expect, it, vi } from 'vitest';
import { mount } from '@vue/test-utils';
import OperationTimeline from './OperationTimeline.vue';
import TimelineEntryItem from './TimelineEntryItem.vue';
import type { Operation, TimelineEntry } from '@/types/operation';

function entry(overrides: Partial<TimelineEntry> = {}): TimelineEntry {
  return {
    id: 'e-1',
    operationId: 'op-1',
    sequence: 1n,
    operationStateVersion: 1n,
    timestamp: '2026-08-19T00:00:00.000Z',
    kind: 'STATE_TRANSITION',
    requestId: '',
    errorCode: '',
    errorMessage: '',
    ackStage: '',
    fromState: 'queued',
    toState: 'running',
    effectFrom: '',
    effectTo: '',
    workloadRef: '',
    ready: 0,
    desired: 0,
    ...overrides,
  };
}

function operation(overrides: Partial<Operation> = {}): Operation {
  return {
    operationId: 'op-1',
    releaseDefinitionId: 'def-1',
    operationType: 'INSTALL',
    state: 'running',
    stateVersion: 1n,
    bundleId: '',
    valuesRevisionId: '',
    expectedRevision: 0,
    targetRevision: 0,
    createdBy: '',
    createdAt: null,
    updatedAt: null,
    terminalAt: null,
    deadline: null,
    lastError: '',
    effectStatus: 'UNSPECIFIED',
    ...overrides,
  };
}

describe('TimelineEntryItem', () => {
  it('renders all five visual kinds with distinct labels', async () => {
    const kinds: Array<[TimelineEntry['kind'], string]> = [
      ['STATE_TRANSITION', '状态转换'],
      ['ACK', '已确认'],
      ['ROLLOUT_PROGRESS', '发布进度'],
      ['ERROR', '错误'],
      ['EMERGENCY_EFFECT_RESOLVED', 'Emergency 生效结果已确认'],
    ];

    for (const [kind, label] of kinds) {
      const wrapper = mount(TimelineEntryItem, {
        props: { entry: entry({ kind }) },
      });
      expect(wrapper.text()).toContain(label);
      wrapper.unmount();
    }
  });

  it('renders rollout progress with ready/desired', () => {
    const wrapper = mount(TimelineEntryItem, {
      props: { entry: entry({ kind: 'ROLLOUT_PROGRESS', workloadRef: 'deployments/app/default', ready: 2, desired: 3 }) },
    });
    expect(wrapper.text()).toContain('2/3');
  });

  it('AC-23: cancel transitions render the state change without the reason text', () => {
    const wrapper = mount(TimelineEntryItem, {
      props: {
        entry: entry({
          kind: 'STATE_TRANSITION',
          fromState: 'running',
          toState: 'cancelling',
          requestId: 'req-secret-reason-here',
        }),
      },
    });
    expect(wrapper.text()).toContain('running');
    expect(wrapper.text()).toContain('cancelling');
    // The reason/request identity must never leak into the timeline.
    expect(wrapper.text()).not.toContain('secret-reason');
  });

  it('AC-04: shows copyable operation/request identities for errors', async () => {
    vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
    const wrapper = mount(TimelineEntryItem, {
      props: {
        entry: entry({
          kind: 'ERROR',
          errorCode: 'rollout_timeout',
          errorMessage: '发布超时',
          requestId: 'req-777',
        }),
      },
    });

    expect(wrapper.text()).toContain('op-1');
    expect(wrapper.text()).toContain('req-777');
    await wrapper.findAll('button')[0]!.trigger('click');
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('op-1');
    // The request identity is copyable too (AC-057-04: request_id).
    await wrapper.findAll('button')[1]!.trigger('click');
    expect(navigator.clipboard.writeText).toHaveBeenLastCalledWith('req-777');
  });
});

describe('OperationTimeline', () => {
  it('renders entries sorted by sequence descending', () => {
    const wrapper = mount(OperationTimeline, {
      props: {
        entries: [entry({ id: 'a', sequence: 3n }), entry({ id: 'b', sequence: 9n }), entry({ id: 'c', sequence: 5n })],
        operation: operation(),
        streamStatus: 'connected',
      },
    });
    const items = wrapper.findAll('.timeline-entry');
    expect(items).toHaveLength(3);
    expect(items[0]!.text()).toContain('9');
  });

  it('shows history gap and truncation notices', () => {
    const wrapper = mount(OperationTimeline, {
      props: {
        entries: [entry()],
        operation: operation(),
        streamStatus: 'connected',
        historyTruncated: true,
        historyGap: true,
      },
    });
    expect(wrapper.text()).toContain('部分历史事件不可用');
    expect(wrapper.text()).toContain('最多 500 条');
  });

  it('shows the emergency waiting note for cancelled + UNKNOWN effect', () => {
    const wrapper = mount(OperationTimeline, {
      props: {
        entries: [],
        operation: operation({
          operationType: 'EMERGENCY',
          state: 'cancelled',
          effectStatus: 'UNKNOWN',
        }),
        streamStatus: 'connected',
        emergencyEffectStatus: 'watching',
      },
    });
    expect(wrapper.text()).toContain('操作已取消，正在等待集群生效结果确认…');
  });

  it('AC-20: resolved APPLIED effect shows 操作已取消', () => {
    const wrapper = mount(OperationTimeline, {
      props: {
        entries: [entry({
          id: 'r-1',
          kind: 'EMERGENCY_EFFECT_RESOLVED',
          effectFrom: 'UNKNOWN',
          effectTo: 'APPLIED',
        })],
        operation: operation({
          operationType: 'EMERGENCY',
          state: 'cancelled',
          effectStatus: 'UNKNOWN',
        }),
        streamStatus: 'connected',
        emergencyEffectStatus: 'resolved',
      },
    });
    expect(wrapper.text()).toContain('操作已取消');
    expect(wrapper.text()).not.toContain('正在等待集群生效结果确认');
    expect(wrapper.text()).not.toContain('变更未生效');
  });

  it('AC-20: resolved NOT_APPLIED effect shows 操作已取消，变更未生效', () => {
    const wrapper = mount(OperationTimeline, {
      props: {
        entries: [entry({
          id: 'r-2',
          kind: 'EMERGENCY_EFFECT_RESOLVED',
          effectFrom: 'UNKNOWN',
          effectTo: 'NOT_APPLIED',
        })],
        operation: operation({
          operationType: 'EMERGENCY',
          state: 'cancelled',
          effectStatus: 'UNKNOWN',
        }),
        streamStatus: 'connected',
        emergencyEffectStatus: 'resolved',
      },
    });
    expect(wrapper.text()).toContain('操作已取消，变更未生效');
  });

  it('renders an empty state when there are no entries', () => {
    const wrapper = mount(OperationTimeline, {
      props: { entries: [], operation: operation(), streamStatus: 'connected' },
    });
    expect(wrapper.text()).toContain('暂无时间线事件');
  });
});

/*
 * UX-020 (W14): every <li> carried tabindex="0", so a 500-entry timeline put 500
 * tab stops between the page and whatever follows it. The entries are read-only
 * list content, not controls — the list announces updates through aria-live
 * instead of making the user tab through it.
 */
describe('OperationTimeline keyboard and live-region contract (UX-020)', () => {
  it('keeps timeline entries out of the tab order and announces the list', () => {
    const wrapper = mount(OperationTimeline, {
      props: {
        entries: [entry({ id: 'a', sequence: 3n }), entry({ id: 'b', sequence: 2n })],
        operation: operation(),
        streamStatus: 'connected',
      },
    });
    for (const item of wrapper.findAll('.timeline-entry')) {
      expect(item.attributes('tabindex')).toBeUndefined();
    }
    expect(wrapper.get('.operation-timeline__list').attributes('aria-live')).toBe('polite');
  });
});

/*
 * W15 (TASK-325): the stream retains up to 500 entries. Above the virtual-list
 * threshold only the rows around the viewport are rendered, and the list reports the
 * full size through aria-setsize/aria-posinset so the position stays truthful for
 * assistive technology. At or below the threshold nothing is windowed: behavior and
 * layout match the previous version, but not byte-for-byte DOM — the scroll wrapper is
 * now always present (an unstyled `<div>` around the `<ol>`), it just carries no
 * styles, no tab stop and no scrollbar below the threshold.
 */
describe('OperationTimeline list capacity (W15)', () => {
  function manyEntries(count: number) {
    return Array.from({ length: count }, (_, index) =>
      entry({ id: `e-${index}`, sequence: BigInt(count - index) }),
    );
  }

  it('renders every entry while the list is small', () => {
    const wrapper = mount(OperationTimeline, {
      props: { entries: manyEntries(3), operation: operation(), streamStatus: 'connected' },
    });

    const viewport = wrapper.get('.operation-timeline__viewport');
    expect(wrapper.findAll('.timeline-entry')).toHaveLength(3);
    expect(viewport.classes()).not.toContain('operation-timeline__viewport--virtual');
    expect(viewport.attributes('tabindex')).toBeUndefined();
    expect(wrapper.get('.timeline-entry').attributes('aria-setsize')).toBeUndefined();
  });

  it('flips to a window exactly at 61 entries, keeping all 60 below it', () => {
    // The threshold is inclusive on the "render everything" side: `virtualized` is
    // `length > 60`, so an off-by-one here would silently re-introduce the render cost
    // one row early or keep 61 rows live. Both sides are asserted against the real
    // component, i.e. the production default, not an injected threshold.
    const atThreshold = mount(OperationTimeline, {
      props: { entries: manyEntries(60), operation: operation(), streamStatus: 'connected' },
    });
    const atThresholdViewport = atThreshold.get('.operation-timeline__viewport');
    expect(atThreshold.findAll('.timeline-entry')).toHaveLength(60);
    expect(atThresholdViewport.classes()).not.toContain('operation-timeline__viewport--virtual');
    expect(atThresholdViewport.attributes('role')).toBeUndefined();
    expect(atThresholdViewport.attributes('tabindex')).toBeUndefined();
    atThreshold.unmount();

    const overThreshold = mount(OperationTimeline, {
      props: { entries: manyEntries(61), operation: operation(), streamStatus: 'connected' },
    });
    const overThresholdViewport = overThreshold.get('.operation-timeline__viewport');
    expect(overThresholdViewport.classes()).toContain('operation-timeline__viewport--virtual');
    expect(overThresholdViewport.attributes('role')).toBe('region');
    expect(overThresholdViewport.attributes('tabindex')).toBe('0');
    expect(overThreshold.findAll('.timeline-entry').length).toBeLessThan(61);
    expect(overThreshold.findAll('.timeline-entry').length).toBeGreaterThan(0);
  });

  it('windows a long timeline, keeps the rows positioned and reports the full size', () => {
    const wrapper = mount(OperationTimeline, {
      props: { entries: manyEntries(100), operation: operation(), streamStatus: 'connected' },
    });

    const viewport = wrapper.get('.operation-timeline__viewport');
    const list = wrapper.get<HTMLElement>('.operation-timeline__list');
    expect(viewport.classes()).toContain('operation-timeline__viewport--virtual');
    expect(viewport.attributes('role')).toBe('region');
    expect(viewport.attributes('tabindex')).toBe('0');
    expect(viewport.attributes('aria-label')).toBe('状态时间线（可滚动）');

    const rendered = wrapper.findAll('.timeline-entry');
    expect(rendered.length).toBeLessThan(100);
    expect(rendered.length).toBeGreaterThan(0);
    // The first rendered row is row 1 of 100, and the DOM keeps the scroll height
    // through padding rather than absolute positioning.
    expect(rendered[0]!.attributes('aria-setsize')).toBe('100');
    expect(rendered[0]!.attributes('aria-posinset')).toBe('1');
    expect(Number.parseInt(list.element.style.paddingTop || '0', 10)).toBe(0);
    expect(Number.parseInt(list.element.style.paddingBottom || '0', 10)).toBeGreaterThan(0);
  });

  it('advances the window and the announced position when the list scrolls', async () => {
    const wrapper = mount(OperationTimeline, {
      props: { entries: manyEntries(100), operation: operation(), streamStatus: 'connected' },
    });

    const viewport = wrapper.get('.operation-timeline__viewport');
    viewport.element.scrollTop = 1440; // 20 rows of 72px (64px estimate + 8px gap)
    await viewport.trigger('scroll');

    const rendered = wrapper.findAll('.timeline-entry');
    expect(rendered.length).toBeLessThan(100);
    expect(Number(rendered[0]!.attributes('aria-posinset'))).toBeGreaterThan(1);
    expect(Number.parseInt(wrapper.get<HTMLElement>('.operation-timeline__list').element.style.paddingTop || '0', 10)).toBeGreaterThan(0);
  });
});
