import { createPinia, setActivePinia } from 'pinia';
import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it, vi } from 'vitest';
import { useEmergencyChangeStore } from '@/stores/emergencyChange';
import type { EmergencyConflictDisplay } from '@/connect/emergency-api';
import type { CandidateArtifactDisplay, EmergencyTargetDisplay } from '@/features/emergency/model';

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
});
