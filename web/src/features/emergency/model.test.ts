import { create } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { describe, expect, it } from 'vitest';
import {
  CandidateArtifactSummarySchema,
  ConvergenceTaskDetailSchema,
  EmergencyAction,
  EmergencyConvergence,
  EmergencyEffectStatus,
  EmergencyResultSchema,
  EmergencyTargetSchema,
  EmergencyTypedValuesSchema,
  RunningOperationDetailSchema,
  WorkloadRefSchema,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import {
  mapCandidateArtifact,
  mapConvergencePolicy,
  mapConvergenceTask,
  mapEffectStatus,
  mapEmergencyResult,
  mapEmergencyTarget,
  mapEmergencyTypedValues,
  mapOpType,
  mapRunningOperation,
  workloadRefToWire,
} from '@/features/emergency/model';

describe('model mapping (generated → display)', () => {
  it('maps EmergencyAction enum values exhaustively', () => {
    expect(mapOpType(EmergencyAction.SET_CONTAINER_IMAGE)).toBe('SET_CONTAINER_IMAGE');
    expect(mapOpType(EmergencyAction.SET_REPLICAS)).toBe('SET_REPLICAS');
    expect(mapOpType(EmergencyAction.SET_APPROVED_ANNOTATION)).toBe('SET_APPROVED_ANNOTATION');
    expect(mapOpType(EmergencyAction.UNSPECIFIED)).toBe('UNSPECIFIED');
  });

  it('maps convergence and effect enums', () => {
    expect(mapConvergencePolicy(EmergencyConvergence.REQUIRE_PROMOTION)).toBe('REQUIRE_PROMOTION');
    expect(mapConvergencePolicy(EmergencyConvergence.REVERT_ON_NEXT_RECONCILE)).toBe('REVERT_ON_NEXT_RECONCILE');
    expect(mapConvergencePolicy(EmergencyConvergence.UNSPECIFIED)).toBe('UNSPECIFIED');
    expect(mapEffectStatus(EmergencyEffectStatus.NOT_STARTED)).toBe('NOT_STARTED');
    expect(mapEffectStatus(EmergencyEffectStatus.UNKNOWN)).toBe('UNKNOWN');
    expect(mapEffectStatus(EmergencyEffectStatus.APPLIED)).toBe('APPLIED');
    expect(mapEffectStatus(EmergencyEffectStatus.NOT_APPLIED)).toBe('NOT_APPLIED');
  });

  it('serializes WorkloadRef to the <gvr.resource>/<namespace>/<name> wire form', () => {
    expect(workloadRefToWire({ kind: 'DEPLOYMENT', namespace: 'ns1', name: 'api', uid: 'u1' })).toBe(
      'deployments/ns1/api',
    );
    expect(workloadRefToWire({ kind: 'STATEFUL_SET', namespace: 'ns1', name: 'db', uid: 'u2' })).toBe(
      'statefulsets/ns1/db',
    );
    expect(workloadRefToWire({ kind: 'DAEMON_SET', namespace: 'ns1', name: 'agent', uid: 'u3' })).toBe(
      'daemonsets/ns1/agent',
    );
    expect(workloadRefToWire({ kind: 'CRON_JOB', namespace: 'ns1', name: 'x', uid: 'u4' })).toBe('cron_job/ns1/x');
  });

  it('maps an EmergencyTarget into per-action display models with availability', () => {
    const target = create(EmergencyTargetSchema, {
      workloadRef: create(WorkloadRefSchema, { kind: 'DEPLOYMENT', namespace: 'ns1', name: 'api', uid: 'u1' }),
      containers: ['app', 'sidecar'],
      supportedOperations: [
        EmergencyAction.SET_CONTAINER_IMAGE,
        EmergencyAction.SET_REPLICAS,
        EmergencyAction.SET_APPROVED_ANNOTATION,
      ],
      promotions: [
        { workloadKind: 'DEPLOYMENT', workloadName: 'api', container: 'app', field: 'image_digest', valuesPath: 'image.app' },
        { workloadKind: 'DEPLOYMENT', workloadName: 'api', container: '', field: 'replicas', valuesPath: 'replicas' },
        { workloadKind: 'DEPLOYMENT', workloadName: 'api', container: '', field: 'tier', valuesPath: 'labels.tier' },
      ],
      currentImageRefs: { app: 'repo/app:v1' },
      currentReplicas: 2,
      currentAnnotations: { tier: 'web' },
      hpaManaged: true,
      maxEmergencyReplicas: 10,
    });

    const display = mapEmergencyTarget(target);
    expect(display.workloadRef).toEqual({ kind: 'DEPLOYMENT', namespace: 'ns1', name: 'api', uid: 'u1' });
    expect(display.imageActions).toHaveLength(2);
    expect(display.imageActions[0]).toMatchObject({
      container: 'app',
      currentImageRef: 'repo/app:v1',
      availability: { available: true },
    });
    expect(display.imageActions[0].promotions).toHaveLength(1);
    expect(display.imageActions[1].promotions).toHaveLength(0);
    expect(display.replicasAction).toMatchObject({
      currentReplicas: 2,
      maxEmergencyReplicas: 10,
      hpaManaged: true,
      availability: { available: false, reasonCode: 'hpa_managed' },
    });
    expect(display.annotationActions).toHaveLength(1);
    expect(display.annotationActions[0]).toMatchObject({ key: 'tier', currentValue: 'web', availability: { available: true } });
  });

  it('marks unsupported replicas as unavailable', () => {
    const target = create(EmergencyTargetSchema, {
      workloadRef: create(WorkloadRefSchema, { kind: 'DEPLOYMENT', namespace: 'ns1', name: 'api', uid: 'u1' }),
      containers: [],
      supportedOperations: [EmergencyAction.SET_CONTAINER_IMAGE],
      promotions: [],
      currentImageRefs: {},
      currentReplicas: 1,
      currentAnnotations: {},
      hpaManaged: false,
      maxEmergencyReplicas: 5,
    });
    const display = mapEmergencyTarget(target);
    expect(display.replicasAction?.availability).toEqual({ available: false, reasonCode: 'unsupported_operation' });
  });

  it('maps typed values unions (image/replicas/annotations/empty)', () => {
    expect(
      mapEmergencyTypedValues(
        create(EmergencyTypedValuesSchema, {
          values: { case: 'imageRefValues', value: { container: 'app', imageReference: 'repo/app:v2' } },
        }),
      ),
    ).toEqual({ case: 'image', container: 'app', imageReference: 'repo/app:v2' });
    expect(
      mapEmergencyTypedValues(
        create(EmergencyTypedValuesSchema, { values: { case: 'replicasValues', value: { replicas: 3 } } }),
      ),
    ).toEqual({ case: 'replicas', replicas: 3 });
    expect(
      mapEmergencyTypedValues(
        create(EmergencyTypedValuesSchema, {
          values: { case: 'annotationValues', value: { annotations: [{ key: 'tier', value: 'web' }] } },
        }),
      ),
    ).toEqual({ case: 'annotations', annotations: [{ key: 'tier', value: 'web' }] });
    expect(mapEmergencyTypedValues(undefined)).toBeNull();
  });

  it('maps EmergencyResult including requested bool, evidence and tasks', () => {
    const result = create(EmergencyResultSchema, {
      opType: EmergencyAction.SET_CONTAINER_IMAGE,
      convergencePolicy: EmergencyConvergence.REQUIRE_PROMOTION,
      before: { values: { case: 'imageRefValues', value: { container: 'app', imageReference: 'repo/app:v1' } } },
      after: { values: { case: 'imageRefValues', value: { container: 'app', imageReference: 'repo/app:v2' } } },
      convergenceTasks: [{ taskId: 't1', status: 'PENDING_PROMOTION' }],
      revertStatus: '',
      reconciledByOperationId: '',
      effectStatus: EmergencyEffectStatus.APPLIED,
      requested: true,
    });
    const display = mapEmergencyResult(result);
    expect(display).toMatchObject({
      opType: 'SET_CONTAINER_IMAGE',
      convergencePolicy: 'REQUIRE_PROMOTION',
      requested: true,
      effectStatus: 'APPLIED',
    });
    expect(display?.before).toEqual({ case: 'image', container: 'app', imageReference: 'repo/app:v1' });
    expect(display?.after).toEqual({ case: 'image', container: 'app', imageReference: 'repo/app:v2' });
    expect(display?.convergenceTasks).toEqual([{ taskId: 't1', status: 'PENDING_PROMOTION' }]);
    expect(mapEmergencyResult(undefined)).toBeNull();
  });

  it('maps ConvergenceTaskDetail with selectable/incompatibility projection', () => {
    const task = create(ConvergenceTaskDetailSchema, {
      taskId: 't1',
      operationId: 'op1',
      opType: EmergencyAction.SET_CONTAINER_IMAGE,
      targetSummary: 'DEPLOYMENT/api, container=app',
      submittedAt: timestampFromDate(new Date('2026-08-22T10:00:00Z')),
      reason: '事故 ID 123 修复镜像',
      activeRevisionId: '',
      activeRevisionStatus: '',
      promotionPaths: ['image.app'],
      selectable: true,
      incompatibilityReason: '',
    });
    const display = mapConvergenceTask(task);
    expect(display).toMatchObject({
      taskId: 't1',
      operationId: 'op1',
      opType: 'SET_CONTAINER_IMAGE',
      reasonDisplay: '事故 ID 123 修复镜像',
      promotionPaths: ['image.app'],
      selectable: true,
      incompatibilityReason: '',
    });
    expect(display.submittedAt).toBe('2026-08-22T10:00:00.000Z');
  });

  it('maps CandidateArtifactSummary and RunningOperationDetail', () => {
    const artifact = create(CandidateArtifactSummarySchema, {
      id: 'a1',
      repository: 'repo/app',
      digest: 'sha256:abc',
      ref: 'repo/app@sha256:abc',
      validatedAt: timestampFromDate(new Date('2026-08-22T09:00:00Z')),
      sourceId: 's1',
    });
    expect(mapCandidateArtifact(artifact)).toEqual({
      id: 'a1',
      repository: 'repo/app',
      digest: 'sha256:abc',
      ref: 'repo/app@sha256:abc',
      validatedAt: '2026-08-22T09:00:00.000Z',
      sourceId: 's1',
    });

    const running = create(RunningOperationDetailSchema, {
      operationId: 'op9',
      type: 'UPGRADE',
      status: 'running',
      startedAt: timestampFromDate(new Date('2026-08-22T08:00:00Z')),
    });
    expect(mapRunningOperation(running)).toEqual({
      operationId: 'op9',
      type: 'UPGRADE',
      status: 'running',
      startedAt: '2026-08-22T08:00:00.000Z',
    });
    expect(mapRunningOperation(undefined)).toBeNull();
  });
});
