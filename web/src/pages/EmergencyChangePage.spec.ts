import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createPinia, setActivePinia } from 'pinia';
import { mount } from '@vue/test-utils';
import { nextTick } from 'vue';
import EmergencyChangePage from './EmergencyChangePage.vue';
import EmergencyAnnotationEditor from '@/components/emergency/EmergencyAnnotationEditor.vue';
import EmergencyArtifactSelector from '@/components/emergency/EmergencyArtifactSelector.vue';
import EmergencyReplicasInput from '@/components/emergency/EmergencyReplicasInput.vue';
import { t } from '@/i18n/messages';
import { useEmergencyChangeStore } from '@/stores/emergencyChange';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import type { CandidateArtifactDisplay, EmergencyTargetDisplay } from '@/features/emergency/model';
import type { EmergencyAuthorizationProjection } from '@/connect/emergency-api';

// The page reads its scope from the route. Empty params make its scope watch
// return early, so the test needs no router and issues no requests.
vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router');
  return {
    ...actual,
    useRoute: () => ({
      params: {}, query: {}, path: '/', fullPath: '/', name: undefined,
      hash: '', matched: [], meta: {}, redirectedFrom: undefined,
    }),
  };
});

function snapshot(): EmergencyAuthorizationProjection {
  return {
    organizationId: 'org1', customerId: 'cust1', bindingActive: true, customerActive: true,
    role: 'release_admin', canExecuteEmergency: true, canResolveEmergency: false,
    canCreateValuesRevision: true, canApproveValuesRevision: true,
    sourceVersion: 3n, policyVersion: 1n, checkpoint: 3n, fresh: true,
    actorId: 'user1', emergencyChangeEnabled: true,
  };
}

// A target with no containers: the read model keeps the D7=A sentinels while no
// FRESH operator observation backs the release. A missing column is NOT the
// reason — ListEmergencyTargets projects the observed containers/image refs off
// the release_inventory row, and only the public release-inventory RPCs omit
// these fields. The page used to render the container/artifact picker
// regardless, producing a dead dropdown that looked usable while the target card
// beside it already reported the image capability as unavailable. It now says so
// instead; the observed target below covers the branch that must render.
function unobservedTarget(): EmergencyTargetDisplay {
  return {
    workloadRef: { kind: 'DEPLOYMENT', namespace: 'ns1', name: 'api', uid: 'u1' },
    containers: [], currentImageRefs: {}, currentAnnotations: {},
    supportedOperations: ['SET_REPLICAS'], promotions: [],
    imageActions: [], annotationActions: [],
    annotationAvailability: { available: false, reasonCode: 'not_observed' },
    currentReplicas: -1, maxEmergencyReplicas: 0, hpaManaged: false, replicasAction: null,
  } as unknown as EmergencyTargetDisplay;
}

describe('EmergencyChangePage container picker', () => {
  beforeEach(() => setActivePinia(createPinia()));

  async function loadAuthorized(): Promise<void> {
    const authorization = useEmergencyAuthorizationStore();
    authorization.configure({ loadSnapshot: async () => snapshot() });
    await authorization.load('org1', 'cust1');
  }

  function mountPage() {
    return mount(EmergencyChangePage, { global: { stubs: { RouterLink: true } } });
  }

  it('states the image capability is unavailable instead of rendering a dead picker', async () => {
    await loadAuthorized();

    const store = useEmergencyChangeStore();
    const target = unobservedTarget();
    store.targets = [target];
    // selectedTargetDisplay matches on the workload uid, so the selection is
    // {uid} rather than the display object itself.
    store.selectedTarget = { uid: 'u1' } as never;

    const wrapper = mountPage();

    expect(wrapper.text()).toContain('镜像变更暂不可用');
    expect(wrapper.findComponent({ name: 'EmergencyArtifactSelector' }).exists()).toBe(false);
  });

  /*
   * TASK-331: once a fresh operator observation backs the release, the read
   * model DOES carry the containers and each container's image ref, and the
   * target advertises the image action. This is the shape
   * ListEmergencyTargets really returns then (containers, per-container
   * current_image_refs, the image_digest promotion mappings).
   */
  function observedImageTarget(): EmergencyTargetDisplay {
    const promotion = (container: string) => ({
      workloadKind: 'DEPLOYMENT',
      workloadName: 'api',
      container,
      field: 'image_digest',
      valuesPath: `image.${container}`,
    });
    return {
      workloadRef: { kind: 'DEPLOYMENT', namespace: 'ns1', name: 'api', uid: 'u1' },
      containers: ['app', 'sidecar'],
      supportedOperations: ['SET_CONTAINER_IMAGE'],
      promotions: [promotion('app'), promotion('sidecar')],
      imageActions: [
        {
          container: 'app',
          currentImageRef: 'registry.example.com/api@sha256:1111',
          availability: { available: true },
          promotions: [promotion('app')],
        },
        {
          container: 'sidecar',
          currentImageRef: 'registry.example.com/sidecar@sha256:2222',
          availability: { available: true },
          promotions: [promotion('sidecar')],
        },
      ],
      replicasAction: {
        currentReplicas: -1,
        maxEmergencyReplicas: 0,
        hpaManaged: false,
        availability: { available: false, reasonCode: 'unsupported_operation' },
        promotions: [],
      },
      annotationActions: [],
      annotationAvailability: { available: false, reasonCode: 'not_observed' },
    };
  }

  function imageArtifact(): CandidateArtifactDisplay {
    return {
      id: 'a1',
      repository: 'registry.example.com/api',
      digest: 'sha256:aaaa',
      ref: 'registry.example.com/api@sha256:aaaa',
      validatedAt: '2026-10-01T09:00:00.000Z',
      sourceId: 's1',
    };
  }

  /*
   * TASK-331 (AC-331-01/02). The picker's non-empty branch had NO page-level
   * coverage: the only container case drives containers=[]. This mounts the real
   * page over an observed target and drives the real selector — pick a container
   * and the store loads THAT container's candidates while the selector shows the
   * OBSERVED current image; pick another and the artifact selection resets.
   * Emptying this fixture's containers (the unobservedTarget shape) makes the
   * picker assertion and the select below fail, so the case is not vacuous.
   */
  it('renders the picker for an observed target, cascading container → artifact and showing the current image', async () => {
    await loadAuthorized();
    const store = useEmergencyChangeStore();
    const loaded: string[] = [];
    store.configure({
      loadArtifacts: async (input) => {
        loaded.push(input.container);
        return input.container === 'app' ? [imageArtifact()] : [];
      },
    });
    const target = observedImageTarget();
    store.targets = [target];
    store.selectTarget(target.workloadRef);
    expect(store.actionKind).toBe('image');

    const wrapper = mountPage();
    const picker = wrapper.findComponent(EmergencyArtifactSelector);
    expect(picker.exists()).toBe(true);
    expect(picker.props('containers')).toEqual(['app', 'sidecar']);
    // Nothing picked yet: no current image to state, and no candidate claim.
    expect(wrapper.text()).not.toContain('当前镜像');
    expect(wrapper.text()).toContain('请先选择容器');

    await picker.find('select').setValue('app');
    await vi.waitFor(() => expect(store.artifacts).toHaveLength(1));
    await nextTick();

    expect(loaded).toEqual(['app']);
    expect(store.selectedContainer).toBe('app');
    expect(wrapper.text()).toContain('当前镜像：registry.example.com/api@sha256:1111');
    expect(wrapper.text()).toContain('sha256:aaaa');

    await picker.find('input[type="radio"]').setValue();
    expect(store.selectedArtifact?.id).toBe('a1');

    // AC-058-10 cascade: a different container clears the chosen artifact, asks
    // the server for that container's candidates, and the current image follows.
    await picker.find('select').setValue('sidecar');
    await vi.waitFor(() => expect(loaded).toEqual(['app', 'sidecar']));
    await nextTick();
    expect(store.selectedArtifact).toBeNull();
    expect(wrapper.text()).toContain('当前镜像：registry.example.com/sidecar@sha256:2222');
    expect(wrapper.text()).not.toContain('registry.example.com/api@sha256:1111');
  });

  /*
   * TASK-273: the replicas and annotation payloads had no production entry at
   * all — the annotation editor was referenced only by its own spec, and
   * replicas had no control. These mount the real page and drive the real
   * component, so "the entry exists" is asserted against the page, not against
   * the component in isolation.
   */
  function replicasOnlyTarget(): EmergencyTargetDisplay {
    const replicasPromotion = {
      workloadKind: 'DEPLOYMENT',
      workloadName: 'api',
      container: '',
      field: 'replicas',
      valuesPath: 'replicas',
    };
    return {
      workloadRef: { kind: 'DEPLOYMENT', namespace: 'ns1', name: 'api', uid: 'u1' },
      containers: [],
      supportedOperations: ['SET_REPLICAS'],
      promotions: [replicasPromotion],
      imageActions: [],
      replicasAction: {
        currentReplicas: 2,
        maxEmergencyReplicas: 8,
        hpaManaged: false,
        availability: { available: true },
        promotions: [replicasPromotion],
      },
      annotationActions: [],
      annotationAvailability: { available: false, reasonCode: 'not_observed' },
    };
  }

  function annotationsOnlyTarget(): EmergencyTargetDisplay {
    const workload = { workloadKind: 'DEPLOYMENT', workloadName: 'api', container: '' };
    return {
      workloadRef: { kind: 'DEPLOYMENT', namespace: 'ns1', name: 'api', uid: 'u1' },
      containers: [],
      supportedOperations: ['SET_APPROVED_ANNOTATION'],
      promotions: [
        { ...workload, field: 'tier', valuesPath: 'labels.tier' },
        { ...workload, field: 'zone', valuesPath: 'labels.zone' },
      ],
      imageActions: [],
      replicasAction: {
        currentReplicas: -1,
        maxEmergencyReplicas: 0,
        hpaManaged: false,
        availability: { available: false, reasonCode: 'unsupported_operation' },
        promotions: [],
      },
      annotationActions: [
        {
          key: 'tier',
          scope: 'WORKLOAD_METADATA',
          currentValue: 'web',
          availability: { available: true },
          promotions: [{ ...workload, field: 'tier', valuesPath: 'labels.tier' }],
        },
        {
          key: 'zone',
          scope: 'WORKLOAD_METADATA',
          currentValue: 'a',
          availability: { available: true },
          promotions: [{ ...workload, field: 'zone', valuesPath: 'labels.zone' }],
        },
      ],
      annotationAvailability: { available: true },
    };
  }

  it('mounts the replicas input for a replicas-only target and feeds the store', async () => {
    await loadAuthorized();
    const store = useEmergencyChangeStore();
    const target = replicasOnlyTarget();
    store.targets = [target];
    store.selectTarget(target.workloadRef);

    const wrapper = mountPage();
    expect(store.actionKind).toBe('replicas');

    const input = wrapper.findComponent(EmergencyReplicasInput);
    expect(input.exists()).toBe(true);
    await input.find('input[type="number"]').setValue('4');
    expect(store.replicasValue).toBe(4);
  });

  it('mounts the annotation editor for an annotations-only target and submits its rows', async () => {
    await loadAuthorized();
    const store = useEmergencyChangeStore();
    const target = annotationsOnlyTarget();
    store.targets = [target];
    store.selectTarget(target.workloadRef);

    const wrapper = mountPage();
    expect(store.actionKind).toBe('annotations');

    const editor = wrapper.findComponent(EmergencyAnnotationEditor);
    expect(editor.exists()).toBe(true);
    expect(editor.props('approvedKeys')).toEqual(['tier', 'zone']);
    expect(editor.props('scope')).toBe('WORKLOAD_METADATA');
    expect(editor.props('availableScopes')).toEqual(['WORKLOAD_METADATA']);
    // TASK-274: the observed subset travels beside the whitelist so the editor
    // can label a key "approved, not yet observed".
    expect(editor.props('observedKeys')).toEqual(['tier', 'zone']);

    // The row is added and edited through the mounted page, so the entry that
    // would be submitted is the one the store holds.
    await editor.find('button').trigger('click');
    expect(store.annotationEntries).toHaveLength(1);
    await editor.find('input.field-input').setValue('web');
    expect(store.annotationEntries[0]).toMatchObject({ key: 'tier', value: 'web', scope: 'WORKLOAD_METADATA' });
    expect(store.annotationValidation.valid).toBe(true);
  });

  /*
   * TASK-274 leftover (independent review 10.1). The page maps the store's
   * `annotationWhitelistState` to one of two notices, and nothing connected that mapping
   * to the editor: swapping the violation/unavailable copies left all 1021 tests green.
   * This drives the real store ref and asserts the prop AND the rendered status text per
   * state, so a swap fails here.
   */
  it('maps each whitelist state to its own notice on the editor', async () => {
    await loadAuthorized();
    const store = useEmergencyChangeStore();
    const target = annotationsOnlyTarget();
    store.targets = [target];
    store.selectTarget(target.workloadRef);

    const wrapper = mountPage();
    const editor = wrapper.findComponent(EmergencyAnnotationEditor);
    const notice = () => wrapper.get('.annotation-editor [role="status"]').text();

    // No problem with the read: the editor is told nothing.
    expect(editor.props('whitelistNotice')).toBeNull();
    expect(wrapper.find('.annotation-editor [role="status"]').exists()).toBe(false);

    store.annotationWhitelistState = 'violation';
    await nextTick();
    expect(editor.props('whitelistNotice')).toBe(t('annotation.whitelist.violation'));
    expect(editor.props('whitelistNotice')).not.toBe(t('annotation.whitelist.unavailable'));
    expect(notice()).toBe(t('annotation.whitelist.violation'));

    store.annotationWhitelistState = 'unavailable';
    await nextTick();
    expect(editor.props('whitelistNotice')).toBe(t('annotation.whitelist.unavailable'));
    expect(notice()).toBe(t('annotation.whitelist.unavailable'));

    store.annotationWhitelistState = 'loaded';
    await nextTick();
    expect(editor.props('whitelistNotice')).toBeNull();
    expect(wrapper.find('.annotation-editor [role="status"]').exists()).toBe(false);
  });
});
