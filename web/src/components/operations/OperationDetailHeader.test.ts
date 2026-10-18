import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import { statusLabel } from '@/i18n/status-labels';
import type { Operation } from '@/types/operation';
import OperationDetailHeader from './OperationDetailHeader.vue';

/*
 * The header's conditional actions are the detail page's only mutating entries. The
 * page used to hold them inline; they now live in this presentational component, so
 * the branches (can cancel / already cancelling / terminal / no write permission)
 * are pinned here by their observable contract: which button renders, whether it is
 * disabled, the completion tooltip, and what the click reports.
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
    ...overrides,
  };
}

function mountHeader(props: {
  operation?: Operation | null;
  releaseName?: string;
  showCancel?: boolean;
  canCancel?: boolean;
  cancelLoading?: boolean;
  isTerminal?: boolean;
}) {
  return mount(OperationDetailHeader, {
    props: {
      operation: props.operation === undefined ? operation() : props.operation,
      releaseName: props.releaseName ?? 'checkout',
      showCancel: props.showCancel ?? true,
      canCancel: props.canCancel ?? true,
      cancelLoading: props.cancelLoading ?? false,
      isTerminal: props.isTerminal ?? false,
    },
  });
}

function buttonWithText(wrapper: ReturnType<typeof mountHeader>, label: string) {
  return wrapper.findAll('button').find((button) => button.text() === label);
}

describe('OperationDetailHeader', () => {
  it('renders the operation identity and state', () => {
    const wrapper = mountHeader({});

    expect(wrapper.get('h1').text()).toBe('checkout');
    expect(wrapper.text()).toContain('op-1');
    expect(wrapper.text()).toContain(`${statusLabel('operationType', 'INSTALL')}操作`);
    expect(wrapper.get('.operation-detail__state').text()).toBe(statusLabel('operation', 'running'));
  });

  it('reports a cancel request and a manual refresh', async () => {
    const wrapper = mountHeader({});

    await buttonWithText(wrapper, '取消操作')!.trigger('click');
    expect(wrapper.emitted('request-cancel')).toHaveLength(1);

    await buttonWithText(wrapper, '刷新')!.trigger('click');
    expect(wrapper.emitted('refresh')).toHaveLength(1);
  });

  it('renders a disabled 取消中… button while the operation is cancelling', () => {
    const wrapper = mountHeader({
      operation: operation({ state: 'cancelling' }),
      canCancel: false,
    });

    const button = buttonWithText(wrapper, '取消中…');
    expect(button).toBeDefined();
    expect(button!.attributes('disabled')).toBeDefined();
    expect(buttonWithText(wrapper, '取消操作')).toBeUndefined();
  });

  it('disables the cancel entry with the completion tooltip for terminal operations', () => {
    const wrapper = mountHeader({
      operation: operation({ state: 'succeeded' }),
      canCancel: false,
      isTerminal: true,
    });

    const button = buttonWithText(wrapper, '取消操作');
    expect(button).toBeDefined();
    expect(button!.attributes('disabled')).toBeDefined();
    expect(button!.attributes('title')).toBe('操作已完成，无法取消');
  });

  it('shows no cancel affordance without the write capability', () => {
    const wrapper = mountHeader({ showCancel: false });

    expect(wrapper.text()).not.toContain('取消操作');
    expect(wrapper.text()).not.toContain('取消中');
    // Refresh stays available: reading is not gated by the write capability.
    expect(buttonWithText(wrapper, '刷新')).toBeDefined();
  });

  it('disables the cancel entry while a cancellation is in flight', () => {
    const wrapper = mountHeader({ cancelLoading: true });

    const button = buttonWithText(wrapper, '取消中…');
    expect(button).toBeDefined();
    expect(button!.attributes('disabled')).toBeDefined();
  });
});
