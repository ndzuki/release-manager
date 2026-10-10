import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import EmergencyActionSelector from '@/components/emergency/EmergencyActionSelector.vue';
import EmergencyArtifactSelector from '@/components/emergency/EmergencyArtifactSelector.vue';
import EmergencyAnnotationEditor from '@/components/emergency/EmergencyAnnotationEditor.vue';
import EmergencyChangeForm from '@/components/emergency/EmergencyChangeForm.vue';
import EmergencyConfirmDialog from '@/components/emergency/EmergencyConfirmDialog.vue';
import EmergencyReplicasInput from '@/components/emergency/EmergencyReplicasInput.vue';
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
    annotationAvailability: { available: false, reasonCode: 'not_observed' },
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

  // Found while running the browser smoke against the real stack: the API returned a
  // verified artifact while the page claimed none existed, because the empty list before a
  // container is chosen has nothing to do with verification.
  it('ArtifactSelector asks for a container before claiming there are no candidates', () => {
    const wrapper = mount(EmergencyArtifactSelector, {
      props: {
        containers: ['app'],
        selectedContainer: '',
        artifacts: [],
        selectedArtifactId: null,
        loading: false,
        error: null,
      },
    });
    expect(wrapper.text()).toContain('请先选择容器');
    expect(wrapper.text()).not.toContain('没有可用的 VERIFIED 候选制品');

    // Once a container is chosen and the server really returns nothing, the honest
    // message is back.
    const empty = mount(EmergencyArtifactSelector, {
      props: {
        containers: ['app'],
        selectedContainer: 'app',
        artifacts: [],
        selectedArtifactId: null,
        loading: false,
        error: null,
      },
    });
    expect(empty.text()).toContain('没有可用的 VERIFIED 候选制品');
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

  /*
   * TASK-275 (A11y subset ②, continued). The reason error used to render INSIDE the
   * wrapping <label> (as `<span class="error-text">`, which TASK-269's <small>-based scan
   * missed), so it was part of the textarea's accessible NAME. FormField (TASK-268) owns
   * the clean shape; the byte counter rides in the slot and is described too. Both halves
   * asserted separately, so dropping either relation fails here.
   */
  it('ChangeForm describes the reason error instead of naming the textarea with it (TASK-275)', () => {
    const wrapper = mount(EmergencyChangeForm, {
      props: {
        reason: '',
        convergencePolicy: 'REQUIRE_PROMOTION',
        requirePromotionAvailable: true,
        mappingComplete: true,
        submitError: null,
      },
    });
    const textarea = wrapper.get('textarea');
    const id = textarea.attributes('id');
    const label = wrapper.get(`label[for="${id}"]`);

    // ① the accessible name is the label copy only — the error is not inside it ...
    expect(label.text()).not.toContain('请填写变更原因');
    expect(label.find('.form-field__error').exists()).toBe(false);
    // ② ... it is the textarea's described alert instead, and the counter stays a hint.
    const describedBy = textarea.attributes('aria-describedby');
    expect(describedBy).toBeTruthy();
    expect(describedBy!.split(' ')).toEqual(expect.arrayContaining([`${id}-error`, 'emergency-reason-byte-count']));
    const errorNode = wrapper.get(`#${id}-error`);
    expect(errorNode.text()).toContain('请填写变更原因');
    expect(errorNode.attributes('role')).toBe('alert');
    expect(textarea.attributes('aria-invalid')).toBe('true');
    expect(wrapper.get('#emergency-reason-byte-count').text()).toContain('0 / 1000 字节');
  });

  it('ChangeForm drops the reason alert and aria-invalid while the reason is valid (TASK-275)', () => {
    const wrapper = mount(EmergencyChangeForm, {
      props: {
        reason: '修复镜像',
        convergencePolicy: 'REQUIRE_PROMOTION',
        requirePromotionAvailable: true,
        mappingComplete: true,
        submitError: null,
      },
    });
    const textarea = wrapper.get('textarea');

    expect(wrapper.find('.form-field__error').exists()).toBe(false);
    expect(textarea.attributes('aria-invalid')).toBeUndefined();
    expect(textarea.attributes('aria-describedby')).toBe('emergency-reason-byte-count');
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

  // TASK-273: the editor carries its own scope selector and exposes the row
  // error to assistive tech (aria-invalid + a role="alert" message referenced by
  // aria-describedby) instead of only painting it red.
  it('AnnotationEditor offers the approved scopes and describes its row error (TASK-273)', async () => {
    const wrapper = mount(EmergencyAnnotationEditor, {
      props: {
        approvedKeys: ['tier'],
        scope: 'WORKLOAD_METADATA',
        availableScopes: ['WORKLOAD_METADATA', 'POD_TEMPLATE_METADATA'],
        values: [{ localId: 'local-1', key: 'tier', value: '', scope: 'WORKLOAD_METADATA' }],
      },
    });

    // The scope selector is NOT a row control: it must not be captured by the
    // row-control selector used by the a11y wiring test above.
    const scopeSelect = wrapper.get('select.scope-select');
    expect(scopeSelect.findAll('option')).toHaveLength(2);
    await scopeSelect.setValue('POD_TEMPLATE_METADATA');
    expect(wrapper.emitted('update:scope')).toEqual([['POD_TEMPLATE_METADATA']]);

    // An empty value is invalid, and the control points at the rendered alert.
    const valueInput = wrapper.get('input.field-input');
    expect(valueInput.attributes('aria-invalid')).toBe('true');
    const describedBy = valueInput.attributes('aria-describedby');
    expect(describedBy).toBeTruthy();
    expect(wrapper.get(`#${describedBy}`).attributes('role')).toBe('alert');
  });

  // TASK-274: with the definition whitelist the editor can offer a key the
  // workload has never carried. The option label must say which keys are
  // approved-but-unobserved, and the fallback notice must be announced.
  it('labels approved-but-unobserved keys and announces the whitelist fallback (TASK-274)', () => {
    const wrapper = mount(EmergencyAnnotationEditor, {
      props: {
        approvedKeys: ['tier', 'owner'],
        scope: 'WORKLOAD_METADATA',
        values: [{ localId: 'local-1', key: 'tier', value: '', scope: 'WORKLOAD_METADATA' }],
        observedKeys: ['tier'],
        whitelistNotice: '注解白名单暂不可用：未能读取该发布定义，这里只列出已观测到的注解键。',
      },
    });

    const options = wrapper.findAll('select.field-input option').map((option) => option.text());
    expect(options).toEqual(['tier（已观测）', 'owner（已批准，未观测）']);
    const notice = wrapper.get('[role="status"]');
    expect(notice.text()).toContain('注解白名单暂不可用');
  });

  // TASK-273: the replicas control exists, renders min=1 (0 means "not
  // requested" on the wire and selects the image branch server-side) and wires
  // the FormField relations for its label/help/error.
  it('ReplicasInput wires its label/error relations and emits the parsed value (TASK-273)', async () => {
    const wrapper = mount(EmergencyReplicasInput, {
      props: { value: 3, currentReplicas: 2, max: 8, available: true, error: '副本数超出允许范围' },
    });

    const input = wrapper.get('input[type="number"]');
    expect(input.attributes('min')).toBe('1');
    expect(input.attributes('max')).toBe('8');
    const id = input.attributes('id');
    expect(id).toBeTruthy();
    expect(wrapper.get(`label[for="${id}"]`).text()).toContain('目标副本数');
    expect(input.attributes('aria-invalid')).toBe('true');
    const describedBy = input.attributes('aria-describedby');
    expect(describedBy).toBeTruthy();
    const alert = wrapper.get('[role="alert"]');
    expect(describedBy).toContain(alert.attributes('id'));
    expect(alert.text()).toContain('副本数超出允许范围');

    await input.setValue('5');
    expect(wrapper.emitted('update:value')).toEqual([[5]]);
    await input.setValue('');
    expect(wrapper.emitted('update:value')?.[1]).toEqual([null]);
  });

  it('ReplicasInput states unavailability instead of rendering a dead control (TASK-273)', () => {
    const wrapper = mount(EmergencyReplicasInput, {
      props: {
        value: null,
        currentReplicas: 2,
        max: 8,
        available: false,
        unavailableReason: '副本数由 HPA 管理，不可修改。',
        error: null,
      },
    });
    expect(wrapper.find('input[type="number"]').exists()).toBe(false);
    expect(wrapper.text()).toContain('副本数由 HPA 管理');
  });

  // TASK-273/TASK-274: one action per request. The selector keeps an unavailable
  // action visible but disabled WITH the reason its own availability carries:
  // "never observed an approved annotation" and "this operator cannot set
  // annotations" are different operator problems, so they must not share copy.
  it('ActionSelector disables unavailable actions with their reason (TASK-273)', async () => {
    const base = target();
    const wrapper = mount(EmergencyActionSelector, { props: { target: base, selected: 'image' } });

    const replicas = wrapper.get('input[value="replicas"]');
    expect((replicas.element as HTMLInputElement).disabled).toBe(true);
    expect(wrapper.text()).toContain('副本数由 HPA 管理');
    const annotations = wrapper.get('input[value="annotations"]');
    expect((annotations.element as HTMLInputElement).disabled).toBe(true);
    // base carries reasonCode 'not_observed'
    expect(wrapper.text()).toContain('尚未观测到该工作负载上的已批准注解');
    expect(wrapper.text()).not.toContain('不支持设置已批准注解');

    // The image action is selectable, so its radio is enabled (and already
    // checked, which is why the emit assertion below uses another action).
    const image = wrapper.get('input[value="image"]');
    expect((image.element as HTMLInputElement).disabled).toBe(false);

    // A different availability cause must produce different copy.
    await wrapper.setProps({
      target: { ...base, annotationAvailability: { available: false, reasonCode: 'unsupported_operation' } },
    });
    expect(wrapper.text()).toContain('不支持设置已批准注解');
    expect(wrapper.text()).not.toContain('尚未观测到该工作负载上的已批准注解');

    await wrapper.setProps({
      target: {
        ...base,
        replicasAction: {
          currentReplicas: 2,
          maxEmergencyReplicas: 8,
          hpaManaged: false,
          availability: { available: true },
          promotions: [],
        },
      },
    });
    await wrapper.get('input[value="replicas"]').setValue();
    expect(wrapper.emitted('update:selected')).toEqual([['replicas']]);
  });

  it('ConfirmDialog requires risk acceptance before submit (AC-058-15/16)', async () => {
    const wrapper = mount(EmergencyConfirmDialog, {
      // Overlay geometry (Teleport/focus trap) is AppDialog's concern and is
      // covered by AppDialog.test.ts; this test is about the confirm flow.
      global: { stubs: { Teleport: true } },
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
    // Re-query after the prop change: the footer button is slotted through
    // AppDialog, so a re-render replaces the node and the earlier reference is
    // detached.
    const enabled = wrapper.find('button.primary');
    expect((enabled.element as HTMLButtonElement).disabled).toBe(false);
    await enabled.trigger('click');
    expect(wrapper.emitted('confirm')).toHaveLength(1);
  });
});
