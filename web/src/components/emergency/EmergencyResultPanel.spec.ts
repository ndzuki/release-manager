import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import EmergencyConvergenceCard from '@/components/emergency/EmergencyConvergenceCard.vue';
import EmergencyResultPanel from '@/components/emergency/EmergencyResultPanel.vue';
import type { EmergencyResultDisplay } from '@/features/emergency/model';

function result(overrides: Partial<EmergencyResultDisplay> = {}): EmergencyResultDisplay {
  return {
    opType: 'SET_CONTAINER_IMAGE',
    convergencePolicy: 'REQUIRE_PROMOTION',
    requested: true,
    before: { case: 'image', container: 'app', imageReference: 'repo/app:v1' },
    after: { case: 'image', container: 'app', imageReference: 'repo/app:v2' },
    effectStatus: 'APPLIED',
    convergenceTasks: [{ taskId: 't1', status: 'PENDING_PROMOTION' }],
    revertStatus: '',
    reconciledByOperationId: '',
    ...overrides,
  };
}

describe('EmergencyResultPanel (AC-058-20~33)', () => {
  it('renders typed intent, evidence and effect status', () => {
    const wrapper = mount(EmergencyResultPanel, {
      props: {
        result: result(),
        operationState: 'succeeded',
        operationEffectStatus: 'APPLIED',
        observationStatus: 'resolved',
        canCreateValuesRevision: false,
      },
    });
    expect(wrapper.text()).toContain('SET_CONTAINER_IMAGE / REQUIRE_PROMOTION');
    expect(wrapper.text()).toContain('目标字段已写入集群');
    expect(wrapper.text()).toContain('repo/app:v2');
    expect(wrapper.text()).toContain('t1');
  });

  it('warns on terminal + UNKNOWN (Unresolved Emergency Effect) and keeps observing (AC-058-23)', () => {
    const wrapper = mount(EmergencyResultPanel, {
      props: {
        result: result({ effectStatus: 'UNKNOWN' }),
        operationState: 'timeout',
        operationEffectStatus: 'UNKNOWN',
        observationStatus: 'watching',
        canCreateValuesRevision: false,
      },
    });
    expect(wrapper.text()).toContain('Unresolved Emergency Effect');
    expect(wrapper.text()).toContain('继续观察');
  });

  it('keeps state and effect orthogonal for queued operations (AC-058-21)', () => {
    const wrapper = mount(EmergencyResultPanel, {
      props: {
        result: result({ effectStatus: 'UNKNOWN', convergenceTasks: [] }),
        operationState: 'queued',
        operationEffectStatus: 'UNKNOWN',
        observationStatus: 'watching',
        canCreateValuesRevision: false,
      },
    });
    expect(wrapper.text()).toContain('集群效果未知');
    // queued is not terminal → no unresolved warning.
    expect(wrapper.text()).not.toContain('Unresolved Emergency Effect');
  });

  it('renders the convergence card CTA only when APPLIED + capable (AC-058-30)', async () => {
    const wrapper = mount(EmergencyResultPanel, {
      props: {
        result: result({ convergenceTasks: [] }),
        operationState: 'succeeded',
        operationEffectStatus: 'APPLIED',
        observationStatus: 'resolved',
        canCreateValuesRevision: true,
      },
    });
    const card = wrapper.findComponent(EmergencyConvergenceCard);
    expect(card.exists()).toBe(true);
    expect(wrapper.text()).toContain('创建 ValuesRevision 收敛');
    await wrapper.find('button').trigger('click');
    expect(wrapper.emitted('open-convergence')).toHaveLength(1);
  });

  it('renders NOT_APPLIED without creating any convergence task (AC-058-31)', () => {
    const wrapper = mount(EmergencyResultPanel, {
      props: {
        result: result({ effectStatus: 'NOT_APPLIED', convergenceTasks: [] }),
        operationState: 'failed',
        operationEffectStatus: 'NOT_APPLIED',
        observationStatus: 'resolved',
        canCreateValuesRevision: true,
      },
    });
    expect(wrapper.text()).toContain('未生效，不创建收敛任务');
    expect(wrapper.text()).not.toContain('创建 ValuesRevision 收敛');
  });
});

describe('EmergencyConvergenceCard (AC-058-32/33)', () => {
  it('shows REVERT awaiting and reconciled labels', () => {
    const awaiting = mount(EmergencyConvergenceCard, {
      props: {
        result: result({
          convergencePolicy: 'REVERT_ON_NEXT_RECONCILE',
          revertStatus: 'AWAITING_STANDARD_RELEASE',
          convergenceTasks: [],
        }),
        applied: true,
        canCreateValuesRevision: false,
      },
    });
    expect(awaiting.text()).toContain('等待标准发布对账');
    expect(awaiting.text()).toContain('不创建收敛任务');

    const reconciled = mount(EmergencyConvergenceCard, {
      props: {
        result: result({
          convergencePolicy: 'REVERT_ON_NEXT_RECONCILE',
          revertStatus: 'RECONCILED',
          reconciledByOperationId: 'op9',
          convergenceTasks: [],
        }),
        applied: true,
        canCreateValuesRevision: false,
      },
    });
    expect(reconciled.text()).toContain('已对账（Operation op9）');
  });

  it('hides the create CTA without capability (AC-058-47)', () => {
    const wrapper = mount(EmergencyConvergenceCard, {
      props: {
        result: result({ convergenceTasks: [] }),
        applied: true,
        canCreateValuesRevision: false,
      },
    });
    expect(wrapper.find('button').exists()).toBe(false);
  });
});
