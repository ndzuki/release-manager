import { describe, expect, it } from 'vitest';
import {
  ANNOTATION_MAX_ENTRIES,
  canonicalIntentJson,
  MAX_TASKS_PER_REVISION,
  REASON_MAX_BYTES,
  REASON_MIN_BYTES,
  utf8ByteLength,
  validateAnnotationEntries,
  validateConvergenceSelection,
  validateReason,
  validateReplicas,
  validateReplicasChange,
  annotationMappingComplete,
  imageMappingComplete,
  replicasMappingComplete,
} from '@/features/emergency/validation';
import type { ConvergenceTaskDisplay, EmergencyTargetDisplay } from '@/features/emergency/model';

function targetWithPromotions(promotions: EmergencyTargetDisplay['promotions']): EmergencyTargetDisplay {
  return {
    workloadRef: { kind: 'DEPLOYMENT', namespace: 'ns1', name: 'api', uid: 'u1' },
    containers: ['app'],
    supportedOperations: ['SET_CONTAINER_IMAGE', 'SET_REPLICAS', 'SET_APPROVED_ANNOTATION'],
    promotions,
    imageActions: [],
    replicasAction: null,
    annotationActions: [],
    annotationAvailability: { available: false, reasonCode: 'not_observed' },
  };
}

describe('UTF-8 byte validation', () => {
  it('counts bytes, not JS code points', () => {
    expect(utf8ByteLength('abc')).toBe(3);
    expect(utf8ByteLength('中文')).toBe(6);
    expect(utf8ByteLength('🚀')).toBe(4);
  });

  // AC-058-12 / TASK-325 W3: the form mirrors the server's 1-1000 UTF-8 BYTE
  // window, so the operator is never told "valid" for a reason the server will
  // reject as invalid_reason. The CJK cases are the byte-vs-rune
  // discriminators: 400 CJK characters are only 400 code points but 1200 bytes.
  it('enforces the 1-1000 UTF-8 byte reason window (AC-058-12)', () => {
    expect(validateReason('  事故 ID 123  ')).toEqual({ valid: true });
    expect(validateReason('   ')).toMatchObject({ valid: false, code: 'reason_required' });
    // One byte is the contract minimum; the old rune window required five.
    expect(validateReason('a')).toEqual({ valid: true });
    expect(validateReason('  a  ')).toEqual({ valid: true });
    expect(validateReason('a'.repeat(REASON_MAX_BYTES))).toEqual({ valid: true });
    expect(validateReason('a'.repeat(REASON_MAX_BYTES + 1))).toMatchObject({ valid: false, code: 'reason_too_long' });
    expect(validateReason('中'.repeat(300))).toEqual({ valid: true }); // 900 bytes
    expect(validateReason('中'.repeat(333))).toEqual({ valid: true }); // 999 bytes
    expect(validateReason('中'.repeat(334))).toMatchObject({ valid: false, code: 'reason_too_long' }); // 1002 bytes
    expect(validateReason('中'.repeat(400))).toEqual({
      valid: false,
      code: 'reason_too_long',
      message: '变更原因最多 1000 字节（当前 1200 字节）',
    });
    expect(validateReason('🚀'.repeat(250))).toEqual({ valid: true }); // 1000 bytes
    expect(validateReason('🚀'.repeat(251))).toMatchObject({ valid: false, code: 'reason_too_long' }); // 1004 bytes
    expect(REASON_MIN_BYTES).toBe(1);
    expect(REASON_MAX_BYTES).toBe(1000);
    expect(validateReason(`nul\u0000char`)).toMatchObject({ valid: false, code: 'reason_invalid_chars' });
    expect(validateReason('abcd\ufffe')).toMatchObject({ valid: false, code: 'reason_invalid_chars' });
    expect(validateReason('abcd\uffff')).toMatchObject({ valid: false, code: 'reason_invalid_chars' });
  });

  it('validates replicas range and HPA gating (AC-058-01)', () => {
    expect(validateReplicas(3, 10, false)).toEqual({ valid: true });
    expect(validateReplicas(0, 10, false)).toEqual({ valid: true });
    expect(validateReplicas(10, 10, false)).toEqual({ valid: true });
    expect(validateReplicas(3, 10, true)).toMatchObject({ valid: false, code: 'hpa_managed' });
    expect(validateReplicas(11, 10, false)).toMatchObject({ valid: false, code: 'invalid_replicas' });
    expect(validateReplicas(-1, 10, false)).toMatchObject({ valid: false, code: 'invalid_replicas' });
    expect(validateReplicas(1.5, 10, false)).toMatchObject({ valid: false, code: 'invalid_replicas' });
  });

  it('validates annotation entry batches (AC-058-13)', () => {
    expect(
      validateAnnotationEntries([{ localId: '1', key: 'tier', value: 'web', scope: 'WORKLOAD_METADATA' }]),
    ).toEqual({ valid: true });
    expect(validateAnnotationEntries([])).toMatchObject({ valid: false, code: 'invalid_annotation_entries' });
    const tooMany = Array.from({ length: ANNOTATION_MAX_ENTRIES + 1 }, (_, i) => ({
      localId: String(i),
      key: `k${i}`,
      value: 'v',
      scope: 'WORKLOAD_METADATA',
    }));
    expect(validateAnnotationEntries(tooMany)).toMatchObject({ valid: false, code: 'invalid_annotation_entries' });
    expect(
      validateAnnotationEntries([
        { localId: '1', key: 'a', value: 'v', scope: 'WORKLOAD_METADATA' },
        { localId: '2', key: 'a', value: 'v2', scope: 'WORKLOAD_METADATA' },
      ]),
    ).toMatchObject({ valid: false, code: 'duplicate_annotation_key' });
    expect(
      validateAnnotationEntries([
        { localId: '1', key: 'a', value: 'v', scope: 'WORKLOAD_METADATA' },
        { localId: '2', key: 'b', value: 'v', scope: 'POD_TEMPLATE_METADATA' },
      ]),
    ).toMatchObject({ valid: false, code: 'annotation_scope_mismatch' });
    expect(
      validateAnnotationEntries([{ localId: '1', key: 'a', value: 'x'.repeat(2049), scope: 'WORKLOAD_METADATA' }]),
    ).toMatchObject({ valid: false, code: 'invalid_annotation_entries' });
    expect(
      validateAnnotationEntries([{ localId: '1', key: 'a', value: 'v\ufffe', scope: 'WORKLOAD_METADATA' }]),
    ).toMatchObject({ valid: false, code: 'invalid_annotation_entries' });
  });

  it('derives promotion mapping completeness per action (AC-058-14)', () => {
    const target = targetWithPromotions([
      { workloadKind: 'DEPLOYMENT', workloadName: 'api', container: 'app', field: 'image_digest', valuesPath: 'image.app' },
      { workloadKind: 'DEPLOYMENT', workloadName: 'api', container: '', field: 'replicas', valuesPath: 'replicas' },
      { workloadKind: 'DEPLOYMENT', workloadName: 'api', container: '', field: 'tier', valuesPath: 'labels.tier' },
    ]);
    expect(imageMappingComplete(target, 'app')).toBe(true);
    expect(imageMappingComplete(target, 'sidecar')).toBe(false);
    expect(replicasMappingComplete(target)).toBe(true);
    expect(annotationMappingComplete(target, ['tier'])).toBe(true);
    expect(annotationMappingComplete(target, ['tier', 'zone'])).toBe(false);
  });

  it('produces a stable canonical intent JSON with sorted locks and no key', () => {
    const json = canonicalIntentJson({
      releaseDefinitionId: 'def1',
      workloadRef: 'deployments/ns1/api',
      actionKind: 'replicas',
      container: '',
      operationVersion: 'v1',
      artifactRef: '',
      setReplicas: 3,
      annotations: [],
      annotationScope: '',
      convergenceStrategy: 'REQUIRE_PROMOTION',
      targetLocks: ['b', 'a'],
    });
    const parsed = JSON.parse(json);
    expect(parsed).toEqual({
      releaseDefinitionId: 'def1',
      workloadRef: 'deployments/ns1/api',
      actionKind: 'replicas',
      container: '',
      operationVersion: 'v1',
      artifactRef: '',
      setReplicas: 3,
      annotations: [],
      annotationScope: '',
      convergenceStrategy: 'REQUIRE_PROMOTION',
      targetLocks: ['a', 'b'],
    });
    expect(json).not.toContain('idempotencyKey');
  });

  // The action and its payload are part of the fingerprint: switching action or
  // editing the entries must change the frozen intent (AC-058-16/17).
  it('folds the selected action payload into the canonical intent', () => {
    const base = {
      releaseDefinitionId: 'def1',
      workloadRef: 'deployments/ns1/api',
      operationVersion: 'v1',
      convergenceStrategy: 'REVERT_ON_NEXT_RECONCILE',
      targetLocks: [],
    };
    const image = canonicalIntentJson({ ...base, actionKind: 'image', container: 'app', artifactRef: 'a1', setReplicas: 0, annotations: [], annotationScope: '' });
    const replicas = canonicalIntentJson({ ...base, actionKind: 'replicas', container: '', artifactRef: '', setReplicas: 3, annotations: [], annotationScope: '' });
    const annotations = canonicalIntentJson({ ...base, actionKind: 'annotations', container: '', artifactRef: '', setReplicas: 0, annotations: [{ key: 'tier', value: 'web' }], annotationScope: 'WORKLOAD_METADATA' });
    expect(new Set([image, replicas, annotations]).size).toBe(3);

    const reordered = canonicalIntentJson({
      ...base,
      actionKind: 'annotations',
      container: '',
      artifactRef: '',
      setReplicas: 0,
      annotations: [{ key: 'zone', value: 'a' }, { key: 'tier', value: 'web' }],
      annotationScope: 'WORKLOAD_METADATA',
    });
    const sameEntries = canonicalIntentJson({
      ...base,
      actionKind: 'annotations',
      container: '',
      artifactRef: '',
      setReplicas: 0,
      annotations: [{ key: 'tier', value: 'web' }, { key: 'zone', value: 'a' }],
      annotationScope: 'WORKLOAD_METADATA',
    });
    expect(reordered).toBe(sameEntries);
  });

  // The submit-level rule differs from the server's accepted RANGE: the flat
  // scalar has no presence, so 0 means "not requested" and must be refused as a
  // replicas change (internal/orchestrator/emergency.go:1095-1099).
  it('refuses replicas 0 / null as a replicas change while validateReplicas still reads it', () => {
    expect(validateReplicas(0, 10, false)).toEqual({ valid: true });
    expect(validateReplicasChange(0, 10, false)).toMatchObject({ valid: false, code: 'invalid_replicas' });
    expect(validateReplicasChange(null, 10, false)).toMatchObject({ valid: false, code: 'invalid_replicas' });
    expect(validateReplicasChange(1, 10, false)).toEqual({ valid: true });
    expect(validateReplicasChange(10, 10, false)).toEqual({ valid: true });
    expect(validateReplicasChange(11, 10, false)).toMatchObject({ valid: false, code: 'invalid_replicas' });
    expect(validateReplicasChange(3, 10, true)).toMatchObject({ valid: false, code: 'hpa_managed' });
  });

  // A key + scope pair outside the definition's whitelist is refused locally
  // with the code the server would emit (annotation_key_not_allowed).
  it('refuses annotation keys outside the approved (key, scope) whitelist', () => {
    const approved = [{ key: 'tier', scope: 'WORKLOAD_METADATA' }];
    expect(
      validateAnnotationEntries([{ localId: '1', key: 'tier', value: 'web', scope: 'WORKLOAD_METADATA' }], approved),
    ).toEqual({ valid: true });
    expect(
      validateAnnotationEntries([{ localId: '1', key: 'zone', value: 'web', scope: 'WORKLOAD_METADATA' }], approved),
    ).toMatchObject({ valid: false, code: 'annotation_key_not_allowed' });
    expect(
      validateAnnotationEntries([{ localId: '1', key: 'tier', value: 'web', scope: 'POD_TEMPLATE_METADATA' }], approved),
    ).toMatchObject({ valid: false, code: 'annotation_key_not_allowed' });
  });
});

describe('convergence selection pre-check (AC-058-34/42)', () => {
  function task(taskId: string, partial: Partial<ConvergenceTaskDisplay> = {}): ConvergenceTaskDisplay {
    return {
      taskId,
      operationId: 'op',
      opType: 'SET_CONTAINER_IMAGE',
      targetSummary: 'DEPLOYMENT/api, container=app',
      submittedAt: null,
      reasonDisplay: 'reason',
      promotionPaths: ['image.app'],
      activeRevisionId: '',
      activeRevisionStatus: '',
      selectable: true,
      incompatibilityReason: '',
      ...partial,
    };
  }

  it('accepts a non-overlapping selection within the 50-task cap', () => {
    const result = validateConvergenceSelection([task('t1', { promotionPaths: ['image.app'] })]);
    expect(result.valid).toBe(true);
    expect(result.conflicts).toHaveLength(0);
  });

  it('rejects empty and over-cap selections', () => {
    expect(validateConvergenceSelection([]).valid).toBe(false);
    const fiftyOne = Array.from({ length: MAX_TASKS_PER_REVISION + 1 }, (_, i) =>
      task(`t${i}`, { promotionPaths: [`p${i}`] }),
    );
    expect(validateConvergenceSelection(fiftyOne).valid).toBe(false);
    const fifty = Array.from({ length: MAX_TASKS_PER_REVISION }, (_, i) =>
      task(`t${i}`, { promotionPaths: [`p${i}`] }),
    );
    expect(validateConvergenceSelection(fifty).valid).toBe(true);
  });

  it('flags overlapping promotion paths and active revisions', () => {
    const result = validateConvergenceSelection([
      task('t1', { promotionPaths: ['image.app'] }),
      task('t2', { promotionPaths: ['image.app', 'replicas'] }),
    ]);
    expect(result.valid).toBe(false);
    expect(result.conflicts[0]).toMatchObject({ taskId: 't2' });
    expect(result.conflicts[0].reason).toContain('t1');

    const bound = validateConvergenceSelection([
      task('t1', { promotionPaths: ['image.app'], activeRevisionId: 'r1' }),
    ]);
    expect(bound.valid).toBe(false);
    expect(bound.conflicts[0]).toMatchObject({ taskId: 't1' });
    expect(bound.conflicts[0].reason).toContain('r1');
  });
});
