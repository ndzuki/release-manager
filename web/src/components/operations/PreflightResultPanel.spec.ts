import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import PreflightResultPanel from './PreflightResultPanel.vue';
import type { PreflightResult } from '@/types/operation';

const failed: PreflightResult = {
  overall: 'failed',
  failedStage: 'render',
  errorCode: 'render_failed',
  stages: [
    { stage: 'artifact', status: 'passed', detail: '' },
    { stage: 'render', status: 'failed', detail: 'render_failed' },
  ],
};

// AC-056-03: the failed stage is highlighted and its checks are expanded.
describe('PreflightResultPanel', () => {
  it('renders nothing while preflight is still in flight', () => {
    const wrapper = mount(PreflightResultPanel, { props: { result: null } });
    expect(wrapper.find('.preflight-panel').exists()).toBe(false);
  });

  it('highlights the failed stage and shows its detail', () => {
    const wrapper = mount(PreflightResultPanel, { props: { result: failed } });

    const failedStage = wrapper.find('[data-testid="preflight-stage-render"]');
    expect(failedStage.exists()).toBe(true);
    expect(failedStage.classes()).toContain('preflight-stage--failed');

    const passedStage = wrapper.find('[data-testid="preflight-stage-artifact"]');
    expect(passedStage.classes()).not.toContain('preflight-stage--failed');

    // The check detail is expanded for the failed stage.
    expect(failedStage.find('[data-testid="preflight-stage-detail"]').text()).toBe('render_failed');
    expect(wrapper.text()).toContain('render_failed');
  });

  it('shows the pipeline error code and a passed overall without a failed stage', () => {
    const wrapper = mount(PreflightResultPanel, {
      props: { result: { overall: 'passed', failedStage: '', errorCode: '', stages: [] } },
    });
    expect(wrapper.text()).toContain('通过');
    expect(wrapper.findAll('.preflight-stage')).toHaveLength(0);
  });
});
