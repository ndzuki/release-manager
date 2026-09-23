import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createPinia, setActivePinia } from 'pinia';
import { mount } from '@vue/test-utils';
import EmergencyChangePage from './EmergencyChangePage.vue';
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
    currentReplicas: -1, maxEmergencyReplicas: 0, hpaManaged: false, replicasAction: null,
  } as unknown as EmergencyTargetDisplay;
}

describe('EmergencyChangePage container picker', () => {
  beforeEach(() => setActivePinia(createPinia()));

  it('states the image capability is unavailable instead of rendering a dead picker', async () => {
    const authorization = useEmergencyAuthorizationStore();
    authorization.configure({ loadSnapshot: async () => snapshot() });
    await authorization.load('org1', 'cust1');

    const store = useEmergencyChangeStore();
    const target = unobservedTarget();
    store.targets = [target];
    // selectedTargetDisplay matches on the workload uid, so the selection is
    // {uid} rather than the display object itself.
    store.selectedTarget = { uid: 'u1' } as never;

    const wrapper = mount(EmergencyChangePage, { global: { stubs: { RouterLink: true } } });

    expect(wrapper.text()).toContain('镜像变更暂不可用');
    expect(wrapper.findComponent({ name: 'EmergencyArtifactSelector' }).exists()).toBe(false);
  });
});
