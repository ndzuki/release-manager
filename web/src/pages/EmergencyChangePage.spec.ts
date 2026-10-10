import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createPinia, setActivePinia } from 'pinia';
import { mount } from '@vue/test-utils';
import EmergencyChangePage from './EmergencyChangePage.vue';
import EmergencyAnnotationEditor from '@/components/emergency/EmergencyAnnotationEditor.vue';
import EmergencyReplicasInput from '@/components/emergency/EmergencyReplicasInput.vue';
import { useEmergencyChangeStore } from '@/stores/emergencyChange';
import { useEmergencyAuthorizationStore } from '@/stores/emergencyAuthorization';
import type { EmergencyTargetDisplay } from '@/features/emergency/model';
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

// The target API currently returns sentinels -- no containers -- because
// release_inventory carries no workload field values (TASK-168). The page used
// to render the container/artifact picker regardless, producing a dead dropdown
// that looked usable while the target card beside it already reported the image
// capability as unavailable. It now says so instead. Rendering the picker
// unconditionally again makes this test fail.
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
});
