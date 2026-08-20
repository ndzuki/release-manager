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
