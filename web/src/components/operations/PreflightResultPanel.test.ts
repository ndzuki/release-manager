import { describe, expect, it } from 'vitest';
import { mount } from '@vue/test-utils';
import PreflightResultPanel from './PreflightResultPanel.vue';
import type { PreflightResult } from '@/types/operation';

/*
 * UX-002 (blocker): the panel used `overall === 'failed' ? '失败' : '通过'` while
 * `overall` is `'passed' | 'failed' | string`. Every other value the server can
 * send — pending, skipped, cancelled, empty — was therefore rendered as 通过, i.e.
 * a check that had not passed was reported as a pass.
 *
 * Server vocabulary (internal/orchestrator/preflight/coordinator.go): skipped is
 * explicitly "not a pass and not a failure", timeout is recorded as cancelled.
 */
function result(overrides: Partial<PreflightResult> = {}): PreflightResult {
  return {
    overall: 'passed',
    failedStage: '',
    stages: [],
    errorCode: '',
    ...overrides,
  };
}

function mountPanel(overrides: Partial<PreflightResult> = {}) {
  return mount(PreflightResultPanel, { props: { result: result(overrides) } });
}

function overall(wrapper: ReturnType<typeof mountPanel>): { text: string; tone: string | undefined } {
  const element = wrapper.get('[data-testid="preflight-overall"]');
  return { text: element.text(), tone: element.attributes('data-tone') };
}

describe('PreflightResultPanel overall state (UX-002)', () => {
  it('never reports 通过 for a state that is not a pass', () => {
    for (const value of ['pending', 'skipped', '', 'running', 'unknown-value']) {
      const view = overall(mountPanel({ overall: value }));
      expect(view.text, `overall=${JSON.stringify(value)}`).not.toContain('通过');
      expect(view.tone, `overall=${JSON.stringify(value)}`).toBe('unknown');
    }
  });

  it('shows the raw server value when the state is unknown', () => {
    expect(overall(mountPanel({ overall: 'pending' })).text).toContain('pending');
    expect(overall(mountPanel({ overall: '' })).text).toContain('未返回状态');
  });

  it('keeps the explicit states explicit', () => {
    expect(overall(mountPanel({ overall: 'passed' }))).toEqual({ text: '通过', tone: 'passed' });
    expect(overall(mountPanel({ overall: 'failed' }))).toEqual({ text: '失败', tone: 'failed' });
    expect(overall(mountPanel({ overall: 'cancelled' }))).toEqual({ text: '已取消', tone: 'cancelled' });
  });

  it('treats a failing stage as failure even when overall claims passed', () => {
    for (const status of ['failed', 'timeout']) {
      const view = overall(
        mountPanel({
          overall: 'passed',
          stages: [{ stage: 'runtime_pull', status, detail: 'image unreachable' }],
        }),
      );
      expect(view.tone, `stage status=${status}`).toBe('failed');
      expect(view.text).toContain('失败');
    }
  });

  it('treats a bare failedStage as failure evidence too', () => {
    const view = overall(mountPanel({ overall: 'passed', failedStage: 'runtime_pull', stages: [] }));
    expect(view.tone).toBe('failed');
    expect(view.text).toContain('失败');
  });

  it('marks the failing stage from either failedStage or the stage statuses', () => {
    // No stages reported: the panel still renders the overall row.
    const byFailedStage = mountPanel({ overall: 'failed', failedStage: 'cluster_dryrun' });
    expect(byFailedStage.find('[data-testid="preflight-stage-0"]').exists()).toBe(false);

    const wrapper = mountPanel({
      overall: 'failed',
      failedStage: 'cluster_dryrun',
      stages: [
        { stage: 'cluster_dryrun', status: 'failed', detail: 'dry-run rejected' },
        { stage: 'helm_render', status: 'passed', detail: '' },
      ],
    });
    expect(wrapper.get('[data-testid="preflight-stage-cluster_dryrun"]').classes()).toContain('preflight-stage--failed');
    expect(wrapper.get('[data-testid="preflight-stage-helm_render"]').classes()).not.toContain('preflight-stage--failed');
  });

  it('keeps showing the stage detail and the error code', () => {
    const wrapper = mountPanel({
      overall: 'failed',
      failedStage: 'runtime_pull',
      errorCode: 'runtime_pull_failed',
      stages: [{ stage: 'runtime_pull', status: 'failed', detail: 'no route to registry' }],
    });
    expect(wrapper.get('[data-testid="preflight-stage-detail"]').text()).toContain('no route to registry');
    expect(wrapper.text()).toContain('runtime_pull_failed');
  });
});
