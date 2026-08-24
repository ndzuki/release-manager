import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import EmergencyArtifactSelector from '@/components/emergency/EmergencyArtifactSelector.vue';
import EmergencyAnnotationEditor from '@/components/emergency/EmergencyAnnotationEditor.vue';
import EmergencyChangeForm from '@/components/emergency/EmergencyChangeForm.vue';
import EmergencyConfirmDialog from '@/components/emergency/EmergencyConfirmDialog.vue';
import EmergencyTargetSelector from '@/components/emergency/EmergencyTargetSelector.vue';
import type { CandidateArtifactDisplay, EmergencyTargetDisplay } from '@/features/emergency/model';

function target(): EmergencyTargetDisplay {
  return {
    workloadRef: { kind: 'DEPLOYMENT', namespace: 'ns1', name: 'api', uid: 'u1' },
    containers: ['app'],
    supportedOperations: ['SET_CONTAINER_IMAGE', 'SET_REPLICAS'],
    promotions: [{ workloadKind: 'DEPLOYMENT', workloadName: 'api', container: 'app', field: 'image_digest', valuesPath: 'image.app' }],
    imageActions: [
      {
        container: 'app',
        currentImageRef: 'repo/app:v1',
        availability: { available: true },
        promotions: [{ workloadKind: 'DEPLOYMENT', workloadName: 'api', container: 'app', field: 'image_digest', valuesPath: 'image.app' }],
      },
    ],
    replicasAction: {
      currentReplicas: 2,
      maxEmergencyReplicas: 10,
      hpaManaged: true,
      availability: { available: false, reasonCode: 'hpa_managed' },
      promotions: [],
    },
    annotationActions: [],
  };
}

function artifact(): CandidateArtifactDisplay {
  return {
    id: 'a1',
    repository: 'repo/app',
    digest: 'sha256:abc',
    ref: 'repo/app@sha256:abc',
    validatedAt: '2026-08-22T09:00:00.000Z',
    sourceId: 's1',
  };
}

describe('emergency components (Step 4)', () => {
  it('TargetSelector renders availability and emits select (AC-058-09)', async () => {
    const wrapper = mount(EmergencyTargetSelector, {
      props: { targets: [target()], selectedUid: null, loading: false, error: null },
    });
    expect(wrapper.text()).toContain('DEPLOYMENT ns1/api');
    expect(wrapper.text()).toContain('副本由 HPA 管理');
    await wrapper.find('input[type="radio"]').setValue();
    expect(wrapper.emitted('select')).toEqual([['u1']]);
  });

  it('ArtifactSelector offers server options only — no digest input (AC-058-10)', async () => {
    const wrapper = mount(EmergencyArtifactSelector, {
      props: {
        containers: ['app'],
        selectedContainer: 'app',
        artifacts: [artifact()],
        selectedArtifactId: null,
        loading: false,
        error: null,
      },
    });
    expect(wrapper.findAll('input[type="text"]')).toHaveLength(0);
    expect(wrapper.text()).toContain('sha256:abc');
    await wrapper.find('input[type="radio"]').setValue();
    expect(wrapper.emitted('select-artifact')).toEqual([['a1']]);
    await wrapper.find('select').setValue('app');
    expect(wrapper.emitted('select-container')).toEqual([['app']]);
  });

  it('ChangeForm gates REQUIRE_PROMOTION on mapping completeness (AC-058-14)', async () => {
    const wrapper = mount(EmergencyChangeForm, {
      props: {
        reason: '修复镜像',
        convergencePolicy: 'REQUIRE_PROMOTION',
        requirePromotionAvailable: false,
        mappingComplete: false,
        submitError: null,
      },
    });
    const requireRadio = wrapper.find('input[value="REQUIRE_PROMOTION"]');
    expect((requireRadio.element as HTMLInputElement).disabled).toBe(true);
    expect(wrapper.text()).toContain('已固定为 REVERT');
    const revertRadio = wrapper.find('input[value="REVERT_ON_NEXT_RECONCILE"]');
    expect((revertRadio.element as HTMLInputElement).checked).toBe(true);
  });

  it('ChangeForm shows field-level reason errors and byte count (AC-058-12)', () => {
    const wrapper = mount(EmergencyChangeForm, {
      props: {
        reason: '',
        convergencePolicy: 'REQUIRE_PROMOTION',
        requirePromotionAvailable: true,
        mappingComplete: true,
        submitError: null,
      },
    });
    expect(wrapper.text()).toContain('请填写变更原因');
    expect(wrapper.text()).toContain('0 / 1000 字节');
  });

  it('AnnotationEditor binds rows to the whitelist and validates duplicates (AC-058-02/13)', async () => {
    const wrapper = mount(EmergencyAnnotationEditor, {
      props: { approvedKeys: ['tier', 'zone'], scope: 'WORKLOAD_METADATA', values: [] },
    });
    await wrapper.find('button').trigger('click');
    expect(wrapper.emitted('update')?.[0]?.[0]).toEqual([
      { localId: 'local-1', key: 'tier', value: '', scope: 'WORKLOAD_METADATA' },
    ]);
    await wrapper.setProps({
      values: [
        { localId: '1', key: 'tier', value: 'a', scope: 'WORKLOAD_METADATA' },
        { localId: '2', key: 'tier', value: 'b', scope: 'WORKLOAD_METADATA' },
      ],
    });
    expect(wrapper.text()).toContain('重复');
  });

  it('ConfirmDialog requires risk acceptance before submit (AC-058-15/16)', async () => {
    const wrapper = mount(EmergencyConfirmDialog, {
      props: {
        open: true,
        workload: target().workloadRef,
        container: 'app',
        artifact: artifact(),
        reason: '修复镜像',
        policy: 'REQUIRE_PROMOTION',
        riskAccepted: false,
        submitting: false,
        error: null,
      },
    });
    const confirm = wrapper.find('button.primary');
    expect((confirm.element as HTMLButtonElement).disabled).toBe(true);
    await wrapper.find('input[type="checkbox"]').setValue();
    expect(wrapper.emitted('update:risk-accepted')).toEqual([[true]]);
    await wrapper.setProps({ riskAccepted: true });
    expect((wrapper.find('button.primary').element as HTMLButtonElement).disabled).toBe(false);
    await confirm.trigger('click');
    expect(wrapper.emitted('confirm')).toHaveLength(1);
  });
});
