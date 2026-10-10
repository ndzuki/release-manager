import { createPinia, setActivePinia } from 'pinia';
import { Code, ConnectError } from '@connectrpc/connect';
import { create, fromBinary } from '@bufbuild/protobuf';
import { describe, expect, it, vi } from 'vitest';
import { useEmergencyChangeStore } from '@/stores/emergencyChange';
import type { EmergencyConflictDisplay } from '@/connect/emergency-api';
import type { DefinitionView } from '@/connect/definition-api';
import { ExecuteEmergencyChangeRequestSchema } from '@/gen/orchestrator/v1/orchestrator_pb';
import { ReleaseDefinitionSchema } from '@/gen/common/v1/domain_pb';
import type { CandidateArtifactDisplay, EmergencyTargetDisplay } from '@/features/emergency/model';

/*
 * The store reads the definition's annotation whitelist on every loadScope
 * (TASK-274). These tests are unit tests of the store, so the definition read is
 * replaced by a seam here; the whitelist test below re-imports the REAL
 * getDefinition and spies the connect client, so the decode and the store
 * consumption are exercised together. A test that needs a non-empty whitelist
 * overrides the seam with `loadDefinition`.
 */
vi.mock('@/connect/definition-api', () => ({
  getDefinition: vi.fn(async (definitionId: string) => ({
    id: definitionId,
    name: '',
    customerId: '',
    clusterId: '',
    namespace: '',
    releaseName: '',
    chartName: '',
    status: '',
    version: 0n,
    hpaManaged: false,
    maxEmergencyReplicas: 0,
    createdAt: null,
    updatedAt: null,
    promotionMappings: [],
    promotionMappingsViolation: null,
    approvedAnnotationKeys: [],
    approvedAnnotationKeysViolation: null,
  })),
}));

function definitionView(overrides: Partial<DefinitionView> = {}): DefinitionView {
  return {
    id: 'def1',
    name: 'def',
    customerId: 'cust1',
    clusterId: 'cluster-1',
    namespace: 'ns',
    releaseName: 'rel',
    chartName: 'chart',
    status: 'active',
    version: 1n,
    hpaManaged: false,
    maxEmergencyReplicas: 8,
    createdAt: null,
    updatedAt: null,
    promotionMappings: [],
    promotionMappingsViolation: null,
    approvedAnnotationKeys: [],
    approvedAnnotationKeysViolation: null,
    ...overrides,
  };
}

function target(overrides: Partial<EmergencyTargetDisplay> = {}): EmergencyTargetDisplay {
  return {
    workloadRef: { kind: 'DEPLOYMENT', namespace: 'ns1', name: 'api', uid: 'u1' },
    containers: ['app', 'sidecar'],
    supportedOperations: ['SET_CONTAINER_IMAGE', 'SET_REPLICAS', 'SET_APPROVED_ANNOTATION'],
    promotions: [
      { workloadKind: 'DEPLOYMENT', workloadName: 'api', container: 'app', field: 'image_digest', valuesPath: 'image.app' },
    ],
    imageActions: [
      {
        container: 'app',
        currentImageRef: 'repo/app:v1',
        availability: { available: true },
        promotions: [{ workloadKind: 'DEPLOYMENT', workloadName: 'api', container: 'app', field: 'image_digest', valuesPath: 'image.app' }],
      },
      {
        container: 'sidecar',
        currentImageRef: 'repo/sidecar:v1',
        availability: { available: true },
        promotions: [],
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
    ...overrides,
  };
}

function artifact(overrides: Partial<CandidateArtifactDisplay> = {}): CandidateArtifactDisplay {
  return {
    id: 'a1',
    repository: 'repo/app',
    digest: 'sha256:abc',
    ref: 'repo/app@sha256:abc',
    validatedAt: '2026-08-22T09:00:00.000Z',
    sourceId: 's1',
    ...overrides,
  };
}

function noConflict(): EmergencyConflictDisplay {
  return { hasConflict: false, runningOperation: null };
}

function conflict(): EmergencyConflictDisplay {
  return {
    hasConflict: true,
    runningOperation: { operationId: 'op9', type: 'UPGRADE', status: 'running', startedAt: null },
  };
}

const SCOPE = { releaseDefinitionId: 'def1', organizationId: 'org1', customerId: 'cust1' };

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe('emergencyChange store', () => {
  // TASK-242: the store used to carry a write-only `selectedOpType` state pinned
  // to SET_CONTAINER_IMAGE. Nothing read it — no component referenced it, and the
  // submit path never sends an operation type (the request carries container /
  // artifactRef and the server derives the operation), while this card's original
  // premise had assumed it could let an unsupported operation through. It was a
  // misleading hardcode, so it was removed.
  //
  // Scope of this guard (review finding): it protects the store's PUBLIC SURFACE
  // only. A future internal `selectedOpType` that is never exposed would not trip
  // it, so it is a reintroduction tripwire, not a proof that no hardcoded
  // operation type exists anywhere in the module.
  it('does not expose the removed write-only operation-type state (TASK-242)', () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    expect('selectedOpType' in store).toBe(false);
  });

  it('blocks the page when CheckEmergencyConflict reports a running standard operation (AC-058-08)', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    const loadTargets = vi.fn();
    store.configure({ loadConflict: async () => conflict(), loadTargets });

    await store.loadScope(SCOPE);
    expect(store.conflict?.hasConflict).toBe(true);
    expect(store.conflict?.runningOperation?.operationId).toBe('op9');
    expect(loadTargets).not.toHaveBeenCalled();
  });

  it('loads targets and cascades container → artifacts with scope fencing', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    const stale = deferred<CandidateArtifactDisplay[]>();
    const fresh = deferred<CandidateArtifactDisplay[]>();
    let artifactCall = 0;
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [target()],
      loadArtifacts: () => {
        artifactCall++;
        return artifactCall === 1 ? stale.promise : fresh.promise;
      },
    });

    await store.loadScope(SCOPE);
    expect(store.targets).toHaveLength(1);
    expect(store.selectedTarget?.uid).toBe('u1');
    expect(store.selectedContainer).toBe('');

    const firstLoad = store.selectContainer('app');
    const secondLoad = store.selectContainer('sidecar');
    fresh.resolve([artifact({ id: 'a2' })]);
    await secondLoad;
    stale.resolve([artifact({ id: 'a1' })]);
    await firstLoad;

    // The stale 'app' response must not overwrite the 'sidecar' selection.
    expect(store.artifacts).toHaveLength(1);
    expect(store.artifacts[0].id).toBe('a2');
  });

  it('freezes the intent: same intent reuses the key, changes mint a new one (AC-058-15/17)', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [target()],
      loadArtifacts: async () => [artifact()],
      randomUUID: (() => {
        let counter = 0;
        return () => `key-${++counter}`;
      })(),
    });

    await store.loadScope(SCOPE);
    store.selectContainer('app');
    await vi.waitFor(() => expect(store.artifacts).toHaveLength(1));
    store.selectArtifact(store.artifacts[0]);
    store.setReason('修复镜像回归');

    store.openConfirm();
    const firstKey = store.confirmedIntent?.idempotencyKey;
    expect(firstKey).toBe('key-1');
    store.closeConfirm();
    store.openConfirm();
    expect(store.confirmedIntent?.idempotencyKey).toBe('key-1');

    store.setReason('修复镜像回归（补充说明）');
    store.openConfirm();
    expect(store.confirmedIntent?.idempotencyKey).toBe('key-2');
    expect(store.intentChanged).toBe(false);
  });

  it('derives effective policy: REQUIRE_PROMOTION degrades to REVERT without mapping (AC-058-14)', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [target()],
      loadArtifacts: async () => [artifact()],
    });

    await store.loadScope(SCOPE);
    store.selectContainer('sidecar'); // no promotion mapping for sidecar
    await vi.waitFor(() => expect(store.artifacts).toHaveLength(1));
    store.selectArtifact(store.artifacts[0]);
    store.setReason('x');
    expect(store.mappingComplete).toBe(false);
    expect(store.effectivePolicy).toBe('REVERT_ON_NEXT_RECONCILE');

    store.selectContainer('app');
    await vi.waitFor(() => expect(store.artifacts).toHaveLength(1));
    store.selectArtifact(store.artifacts[0]);
    store.setConvergencePolicy('REQUIRE_PROMOTION');
    expect(store.mappingComplete).toBe(true);
    expect(store.effectivePolicy).toBe('REQUIRE_PROMOTION');
  });

  it('submits the frozen intent with the bound key and records the authoritative version (AC-058-19)', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    const execute = vi.fn().mockResolvedValue({ operationId: 'op1', operationVersion: 'v2' });
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [target()],
      loadArtifacts: async () => [artifact()],
      execute,
    });

    await store.loadScope(SCOPE);
    store.selectContainer('app');
    await vi.waitFor(() => expect(store.artifacts).toHaveLength(1));
    store.selectArtifact(store.artifacts[0]);
    store.setReason('修复镜像回归');
    store.openConfirm();
    store.setRiskAccepted(true);
    const result = await store.submit();
    expect(result?.operationId).toBe('op1');
    expect(store.operationVersion).toBe('v2');
    expect(execute).toHaveBeenCalledTimes(1);
    const input = execute.mock.calls[0][0];
    expect(input.idempotencyKey).toBeDefined();
    expect(input.workloadRef).toBe('deployments/ns1/api');
    expect(input.convergenceStrategy).toBe('REQUIRE_PROMOTION');
    expect(input.targetLocks).toEqual(['image.app']);
  });

  it('keeps the key on network errors but invalidates it on deterministic rejections (AC-058-15/16)', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    const execute = vi
      .fn()
      .mockRejectedValueOnce(new ConnectError('down', Code.Unavailable))
      .mockResolvedValueOnce({ operationId: 'op1', operationVersion: 'v1' });
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [target()],
      loadArtifacts: async () => [artifact()],
      execute,
    });

    await store.loadScope(SCOPE);
    store.selectContainer('app');
    await vi.waitFor(() => expect(store.artifacts).toHaveLength(1));
    store.selectArtifact(store.artifacts[0]);
    store.setReason('x');
    store.openConfirm();
    const key = store.confirmedIntent?.idempotencyKey;
    store.setRiskAccepted(true);

    await store.submit();
    expect(store.submitError?.code).toBe('network_error');
    expect(store.confirmedIntent?.idempotencyKey).toBe(key); // transient → reuse

    store.openConfirm();
    store.setRiskAccepted(true);
    await store.submit();
    expect(store.operationVersion).toBe('v1');
  });

  it('invalidates the key after idempotency_conflict (AC-058-16)', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    const conflictError = new ConnectError('conflict', Code.AlreadyExists, new Headers({ 'X-Reason-Code': 'idempotency_conflict' }));
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [target()],
      loadArtifacts: async () => [artifact()],
      execute: vi.fn().mockRejectedValue(conflictError),
    });

    await store.loadScope(SCOPE);
    store.selectContainer('app');
    await vi.waitFor(() => expect(store.artifacts).toHaveLength(1));
    store.selectArtifact(store.artifacts[0]);
    store.setReason('x');
    store.openConfirm();
    const firstKey = store.confirmedIntent?.idempotencyKey;
    store.setRiskAccepted(true);
    await store.submit();
    expect(store.submitError?.code).toBe('idempotency_conflict');
    expect(store.confirmedIntent).toBeNull();

    store.openConfirm();
    expect(store.confirmedIntent?.idempotencyKey).not.toBe(firstKey);
  });

  // TASK-014: `target_changed` has no producer on the server (grep in internal/
  // and cmd/ is empty), so the store must not special-case it as a
  // key-invalidating rejection. It behaves like any other unrecognised code and
  // the frozen key is kept for retry. Re-adding it to KEY_INVALIDATING_CODES
  // makes this assertion fail on purpose.
  it('keeps the frozen key for the unproduced target_changed code (TASK-014)', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    const changedError = new ConnectError(
      'changed',
      Code.FailedPrecondition,
      new Headers({ 'X-Reason-Code': 'target_changed' }),
    );
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [target()],
      loadArtifacts: async () => [artifact()],
      execute: vi.fn().mockRejectedValue(changedError),
    });

    await store.loadScope(SCOPE);
    store.selectContainer('app');
    await vi.waitFor(() => expect(store.artifacts).toHaveLength(1));
    store.selectArtifact(store.artifacts[0]);
    store.setReason('x');
    store.openConfirm();
    const key = store.confirmedIntent?.idempotencyKey;
    store.setRiskAccepted(true);

    await store.submit();
    expect(store.submitError?.code).toBe('target_changed');
    expect(store.confirmedIntent?.idempotencyKey).toBe(key); // not a key-invalidating code
  });

  it('clears the selection and reloads candidates after artifact_not_trusted (AC-058-11)', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    const untrusted = new ConnectError(
      'untrusted',
      Code.FailedPrecondition,
      new Headers({ 'X-Reason-Code': 'artifact_not_trusted' }),
    );
    const loadArtifacts = vi.fn(async () => [artifact()]);
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [target()],
      loadArtifacts,
      execute: vi.fn().mockRejectedValue(untrusted),
    });

    await store.loadScope(SCOPE);
    store.selectContainer('app');
    await vi.waitFor(() => expect(store.artifacts).toHaveLength(1));
    store.selectArtifact(store.artifacts[0]);
    store.setReason('x');
    store.openConfirm();
    store.setRiskAccepted(true);
    const callsBeforeSubmit = loadArtifacts.mock.calls.length;

    await store.submit();

    expect(store.submitError?.code).toBe('artifact_not_trusted');
    // The listed artifact passed verification but its trust was revoked before
    // submit: the stale selection must be dropped and the candidates refreshed.
    expect(loadArtifacts.mock.calls.length).toBeGreaterThan(callsBeforeSubmit);
    expect(store.selectedArtifact).toBeNull();
    expect(store.confirmedIntent).toBeNull();
  });

  it('requires container + artifact + valid reason before confirmation', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [target()],
      loadArtifacts: async () => [artifact()],
    });

    await store.loadScope(SCOPE);
    expect(store.canConfirm).toBe(false);
    store.selectContainer('app');
    await vi.waitFor(() => expect(store.artifacts).toHaveLength(1));
    expect(store.canConfirm).toBe(false); // no artifact selected yet
    store.selectArtifact(store.artifacts[0]);
    expect(store.canConfirm).toBe(false); // reason empty
    store.setReason('  ');
    expect(store.canConfirm).toBe(false);
    store.setReason('修复');
    expect(store.canConfirm).toBe(true);
  });

  // ---------------------------------------------------------------------------
  // TASK-273: replicas + annotations actions. The backend has carried both
  // (ExecuteEmergencyChangeRequest.set_replicas/annotations/annotation_scope)
  // since REQ-081/REQ-058, but the console only ever sent the image branch.
  // ---------------------------------------------------------------------------

  function multiActionTarget(overrides: Partial<EmergencyTargetDisplay> = {}): EmergencyTargetDisplay {
    const workload = { workloadKind: 'DEPLOYMENT', workloadName: 'api', container: '' };
    return target({
      supportedOperations: ['SET_CONTAINER_IMAGE', 'SET_REPLICAS', 'SET_APPROVED_ANNOTATION'],
      replicasAction: {
        currentReplicas: 2,
        maxEmergencyReplicas: 8,
        hpaManaged: false,
        availability: { available: true },
        promotions: [{ ...workload, field: 'replicas', valuesPath: 'replicas' }],
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
      promotions: [
        { workloadKind: 'DEPLOYMENT', workloadName: 'api', container: 'app', field: 'image_digest', valuesPath: 'image.app' },
        { ...workload, field: 'replicas', valuesPath: 'replicas' },
        { ...workload, field: 'tier', valuesPath: 'labels.tier' },
        { ...workload, field: 'zone', valuesPath: 'labels.zone' },
      ],
      ...overrides,
    });
  }

  /** Stubs fetch and decodes the binary ExecuteEmergencyChangeRequest body. */
  function stubEmergencyExecuteFetch() {
    const calls: Uint8Array[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const request = new Request(input as RequestInfo, init);
        calls.push(new Uint8Array(await request.arrayBuffer()));
        return new Response(JSON.stringify({ code: 'internal', message: 'boom' }), {
          status: 500,
          headers: { 'Content-Type': 'application/json' },
        });
      }),
    );
    return calls;
  }

  function decode(call: Uint8Array) {
    return fromBinary(ExecuteEmergencyChangeRequestSchema, call);
  }

  it('selects the first available action and resets per-action payload on target change', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    /*
     * Two targets, because the name claims a SWITCH: one target alone cannot
     * show that per-target payload state is dropped (TASK-273 review). The
     * second advertises replicas first and approves a different scope, so the
     * reset is observable in every field the first target left behind.
     */
    const first = multiActionTarget();
    const second = multiActionTarget({
      workloadRef: { kind: 'DEPLOYMENT', namespace: 'ns1', name: 'worker', uid: 'u2' },
      containers: [],
      imageActions: [],
      annotationActions: [],
      annotationAvailability: { available: false, reasonCode: 'not_observed' },
      promotions: [
        { workloadKind: 'DEPLOYMENT', workloadName: 'worker', container: '', field: 'replicas', valuesPath: 'worker.replicas' },
      ],
      replicasAction: {
        currentReplicas: 1,
        maxEmergencyReplicas: 5,
        hpaManaged: false,
        availability: { available: true },
        promotions: [
          { workloadKind: 'DEPLOYMENT', workloadName: 'worker', container: '', field: 'replicas', valuesPath: 'worker.replicas' },
        ],
      },
    });
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [first, second],
      loadArtifacts: async () => [artifact()],
    });

    await store.loadScope(SCOPE);
    // Two targets are not auto-selected; the operator picks one.
    expect(store.selectedTarget).toBeNull();
    store.selectTarget(first.workloadRef);
    // Image is advertised (containers: ['app', 'sidecar']), so it stays first.
    expect(store.actionKind).toBe('image');
    expect(store.availableActions).toEqual(['image', 'replicas', 'annotations']);

    store.setReplicas(4);
    store.setAnnotationEntries([{ localId: 'l1', key: 'tier', value: 'web', scope: 'WORKLOAD_METADATA' }]);
    // The annotation editor anchors on the first approved scope.
    expect(store.annotationScope).toBe('WORKLOAD_METADATA');

    store.selectTarget(second.workloadRef);
    // Switching target drops the first target's payloads and re-anchors the
    // action to the FIRST one the new target advertises.
    expect(store.selectedTarget?.uid).toBe('u2');
    expect(store.actionKind).toBe('replicas');
    expect(store.replicasValue).toBeNull();
    expect(store.annotationEntries).toEqual([]);
    expect(store.annotationScope).toBe('');
  });

  it('submits only the replicas action when it is selected', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    const execute = vi.fn().mockResolvedValue({ operationId: 'op1', operationVersion: 'v1' });
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [multiActionTarget()],
      loadArtifacts: async () => [artifact()],
      execute,
      randomUUID: () => 'key-replicas',
    });

    await store.loadScope(SCOPE);
    store.setActionKind('replicas');
    store.setReplicas(4);
    store.setReason('扩容以缓解事故');
    expect(store.canConfirm).toBe(true);
    store.openConfirm();
    store.setRiskAccepted(true);
    await store.submit();

    const input = execute.mock.calls[0][0];
    expect(input.action).toBe('replicas');
    expect(input.setReplicas).toBe(4);
    expect(input.targetLocks).toEqual(['replicas']);
    expect(input.idempotencyKey).toBe('key-replicas');
  });

  it('submits approved annotations with their scope when the annotations action is selected', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    const execute = vi.fn().mockResolvedValue({ operationId: 'op1', operationVersion: 'v1' });
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [multiActionTarget()],
      loadArtifacts: async () => [artifact()],
      execute,
    });

    await store.loadScope(SCOPE);
    store.setActionKind('annotations');
    store.setAnnotationEntries([
      { localId: 'l1', key: 'tier', value: 'web', scope: 'WORKLOAD_METADATA' },
      { localId: 'l2', key: 'zone', value: 'a', scope: 'WORKLOAD_METADATA' },
    ]);
    store.setReason('修正注解');
    store.openConfirm();
    store.setRiskAccepted(true);
    await store.submit();

    const input = execute.mock.calls[0][0];
    expect(input.action).toBe('annotations');
    expect(input.annotationScope).toBe('WORKLOAD_METADATA');
    expect(input.annotations).toEqual([
      { key: 'tier', value: 'web' },
      { key: 'zone', value: 'a' },
    ]);
    expect(input.targetLocks.sort()).toEqual(['labels.tier', 'labels.zone']);
  });

  // ---------------------------------------------------------------------------
  // TASK-274: the definition's `approved_annotation_keys` whitelist is the read
  // that carries keys approved but not yet OBSERVED on the workload. The target
  // read model only projects the intersection, so before this the editor could
  // not add a single key that was not already on the workload.
  // ---------------------------------------------------------------------------

  it('offers an approved-but-unobserved key and sends it on the wire', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    const calls = stubEmergencyExecuteFetch();
    /*
     * Real decode + store consumption: the injected seam is the connect client
     * call, not a hand-built view, so making decodeApprovedAnnotationKeys return
     * nothing makes this test red (mutation evidence, TASK-274).
     */
    const { getDefinition } = await vi.importActual<typeof import('@/connect/definition-api')>(
      '@/connect/definition-api',
    );
    const client = await import('@/connect/client');
    const spy = vi.spyOn(client.orchestratorClient, 'getReleaseDefinition').mockResolvedValue({
      definition: create(ReleaseDefinitionSchema, {
        id: 'def1',
        approvedAnnotationKeys: new TextEncoder().encode(
          '[{"key":"owner","scope":"WORKLOAD_METADATA"},{"key":"zone","scope":"POD_TEMPLATE_METADATA"}]',
        ),
      }),
    } as never);
    // The unobserved key carries a promotion mapping, so mapping completeness
    // and the lock set have to agree for it too.
    const target = multiActionTarget();
    target.promotions.push({
      workloadKind: 'DEPLOYMENT',
      workloadName: 'api',
      container: '',
      field: 'owner',
      valuesPath: 'labels.owner',
    });
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [target],
      loadArtifacts: async () => [artifact()],
      loadDefinition: getDefinition,
    });

    await store.loadScope(SCOPE);
    expect(store.annotationWhitelistState).toBe('loaded');
    // The whitelist-only scope is offered, and the observed scope stays first.
    expect(store.annotationScopesAvailable).toEqual(['WORKLOAD_METADATA', 'POD_TEMPLATE_METADATA']);

    store.setActionKind('annotations');
    store.setAnnotationScope('WORKLOAD_METADATA');
    // `owner` is approved and not observed; `tier`/`zone` are observed. The
    // whitelist supplies owner, the read model supplies the other two.
    expect(store.approvedAnnotationKeys.map((entry) => entry.key)).toEqual(['owner', 'tier', 'zone']);
    // The observed subset stays smaller — this is what the editor labels
    // "approved, not yet observed".
    expect(store.observedAnnotationKeys).toEqual(['tier', 'zone']);

    store.setAnnotationEntries([{ localId: 'l1', key: 'owner', value: 'platform', scope: 'WORKLOAD_METADATA' }]);
    store.setReason('补充归属注解');
    expect(store.annotationValidation.valid).toBe(true);
    expect(store.mappingComplete).toBe(true);
    expect(store.canConfirm).toBe(true);
    store.openConfirm();
    store.setRiskAccepted(true);
    await store.submit();

    const sent = decode(calls[0]!);
    expect(sent.annotationScope).toBe('WORKLOAD_METADATA');
    expect(sent.annotations.map((entry) => ({ key: entry.key, value: entry.value }))).toEqual([
      { key: 'owner', value: 'platform' },
    ]);
    expect(sent.targetLocks).toEqual(['labels.owner']);
    vi.unstubAllGlobals();
    spy.mockRestore();
  });

  it('falls back to the observed keys and reports a failed whitelist read', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [multiActionTarget()],
      loadArtifacts: async () => [artifact()],
      loadDefinition: async () => {
        throw new ConnectError('down', Code.Unavailable);
      },
    });

    await store.loadScope(SCOPE);

    expect(store.annotationWhitelistState).toBe('unavailable');
    store.setActionKind('annotations');
    store.setAnnotationScope('WORKLOAD_METADATA');
    // Narrower, never wider: the observed projection is the fallback.
    expect(store.approvedAnnotationKeys.map((entry) => entry.key)).toEqual(['tier', 'zone']);
  });

  it('reports an undecodable whitelist as a violation instead of an empty one', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [multiActionTarget()],
      loadArtifacts: async () => [artifact()],
      loadDefinition: async () =>
        definitionView({ approvedAnnotationKeysViolation: '无法解析服务端返回的 JSON（…）' }),
    });

    await store.loadScope(SCOPE);

    expect(store.annotationWhitelistState).toBe('violation');
    store.setActionKind('annotations');
    store.setAnnotationScope('WORKLOAD_METADATA');
    expect(store.approvedAnnotationKeys.map((entry) => entry.key)).toEqual(['tier', 'zone']);
  });

  // The "combination" state and its connect input: with BOTH payloads filled in
  // the form, the selected action alone reaches the wire — the server refuses
  // two actions with conflicting_change, so state from the other branch must
  // never leak. This goes through the REAL executeEmergencyChange and decodes
  // the binary request body (no execute seam, no mocked connect call).
  it('sends exactly one action when both replicas and annotation payloads are filled', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    const calls = stubEmergencyExecuteFetch();
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [multiActionTarget()],
      loadArtifacts: async () => [artifact()],
    });

    await store.loadScope(SCOPE);
    store.setReplicas(5);
    store.setAnnotationEntries([{ localId: 'l1', key: 'tier', value: 'web', scope: 'WORKLOAD_METADATA' }]);
    store.setReason('组合态');

    store.setActionKind('annotations');
    store.openConfirm();
    store.setRiskAccepted(true);
    await store.submit();
    const annotationsWire = decode(calls[0]!);
    expect(annotationsWire.annotationScope).toBe('WORKLOAD_METADATA');
    expect(annotationsWire.annotations.map((entry) => ({ key: entry.key, value: entry.value }))).toEqual([
      { key: 'tier', value: 'web' },
    ]);
    expect(annotationsWire.setReplicas).toBe(0);
    expect(annotationsWire.container).toBe('');
    expect(annotationsWire.artifactRef).toBe('');

    store.setActionKind('replicas');
    store.openConfirm();
    store.setRiskAccepted(true);
    await store.submit();
    const replicasWire = decode(calls[1]!);
    expect(replicasWire.setReplicas).toBe(5);
    expect(replicasWire.annotations).toEqual([]);
    expect(replicasWire.annotationScope).toBe('');
    expect(replicasWire.container).toBe('');
    expect(replicasWire.artifactRef).toBe('');
    vi.unstubAllGlobals();
  });

  it('refuses an out-of-range or unrequested replicas value before confirmation', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [multiActionTarget()],
      loadArtifacts: async () => [artifact()],
    });

    await store.loadScope(SCOPE);
    store.setActionKind('replicas');
    store.setReason('扩容');
    store.setReplicas(9); // > max 8
    expect(store.canConfirm).toBe(false);
    expect(store.replicasError).not.toBeNull();
    store.setReplicas(0); // 0 = "not requested": it would select the image branch
    expect(store.canConfirm).toBe(false);
    store.setReplicas(3);
    expect(store.canConfirm).toBe(true);
  });

  it('keeps HPA-managed replicas unselectable and surfaces the reason', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [
        multiActionTarget({
          replicasAction: {
            currentReplicas: 2,
            maxEmergencyReplicas: 8,
            hpaManaged: true,
            availability: { available: false, reasonCode: 'hpa_managed' },
            promotions: [],
          },
        }),
      ],
      loadArtifacts: async () => [artifact()],
    });

    await store.loadScope(SCOPE);
    expect(store.availableActions).not.toContain('replicas');
    expect(store.replicasAvailable).toBe(false);
    store.setReplicas(1);
    expect(store.replicasError).toBe('副本数由 HPA 管理，不可修改');
  });

  it('rejects annotation entries outside the whitelist and outside one scope', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [multiActionTarget()],
      loadArtifacts: async () => [artifact()],
    });

    await store.loadScope(SCOPE);
    store.setActionKind('annotations');
    store.setReason('注解');

    expect(store.canConfirm).toBe(false); // no rows yet
    store.setAnnotationEntries([{ localId: 'l1', key: 'not-approved', value: 'x', scope: 'WORKLOAD_METADATA' }]);
    expect(store.canConfirm).toBe(false);
    expect(store.annotationValidation).toMatchObject({ valid: false, code: 'annotation_key_not_allowed' });

    // Switching scope drops rows the new scope does not approve.
    store.setAnnotationEntries([{ localId: 'l1', key: 'tier', value: 'x', scope: 'WORKLOAD_METADATA' }]);
    store.setAnnotationScope('POD_TEMPLATE_METADATA');
    expect(store.annotationEntries).toEqual([]);

    store.setAnnotationScope('WORKLOAD_METADATA');
    store.setAnnotationEntries([{ localId: 'l1', key: 'tier', value: 'web', scope: 'WORKLOAD_METADATA' }]);
    expect(store.canConfirm).toBe(true);
  });

  it('routes the stable replicas/annotation server codes to a field error and mints a new key', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    const invalid = new ConnectError('bad', Code.InvalidArgument, new Headers({ 'X-Reason-Code': 'invalid_replicas' }));
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [multiActionTarget()],
      loadArtifacts: async () => [artifact()],
      execute: vi.fn().mockRejectedValue(invalid),
      randomUUID: (() => {
        let counter = 0;
        return () => `key-${++counter}`;
      })(),
    });

    await store.loadScope(SCOPE);
    store.setActionKind('replicas');
    store.setReplicas(3);
    store.setReason('扩容');
    store.openConfirm();
    const firstKey = store.confirmedIntent?.idempotencyKey;
    store.setRiskAccepted(true);
    await store.submit();

    expect(store.submitError?.code).toBe('invalid_replicas');
    expect(store.replicasError).toBe('副本数超出允许范围');
    // Deterministic rejection: the frozen key is dropped.
    expect(store.confirmedIntent).toBeNull();
    store.openConfirm();
    expect(store.confirmedIntent?.idempotencyKey).not.toBe(firstKey);
  });

  it('maps the annotation branch-exclusivity code to a readable message', async () => {
    setActivePinia(createPinia());
    const store = useEmergencyChangeStore();
    const conflicting = new ConnectError(
      'conflict',
      Code.InvalidArgument,
      new Headers({ 'X-Reason-Code': 'conflicting_change' }),
    );
    store.configure({
      loadConflict: async () => noConflict(),
      loadTargets: async () => [multiActionTarget()],
      loadArtifacts: async () => [artifact()],
      execute: vi.fn().mockRejectedValue(conflicting),
    });

    await store.loadScope(SCOPE);
    store.setActionKind('annotations');
    store.setAnnotationEntries([{ localId: 'l1', key: 'tier', value: 'web', scope: 'WORKLOAD_METADATA' }]);
    store.setReason('注解');
    store.openConfirm();
    store.setRiskAccepted(true);
    await store.submit();

    expect(store.submitError?.code).toBe('conflicting_change');
    expect(store.submitError?.message).toBe('同一次请求只能包含一种变更动作');
  });
});
