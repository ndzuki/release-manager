import { describe, expect, it } from 'vitest';
import {
  ANNOTATION_MAX_ENTRIES,
  canonicalIntentJson,
  MAX_TASKS_PER_REVISION,
  REASON_MAX_BYTES,
  utf8ByteLength,
  validateAnnotationEntries,
  validateConvergenceSelection,
  validateReason,
  validateReplicas,
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
  };
}

describe('UTF-8 byte validation', () => {
  it('counts bytes, not JS code points', () => {
    expect(utf8ByteLength('abc')).toBe(3);
    expect(utf8ByteLength('中文')).toBe(6);
    expect(utf8ByteLength('🚀')).toBe(4);
  });

  it('validates reason length/forbidden characters (AC-058-12)', () => {
    expect(validateReason('  事故 ID 123  ')).toEqual({ valid: true });
    expect(validateReason('   ')).toMatchObject({ valid: false, code: 'reason_required' });
    expect(validateReason('a'.repeat(REASON_MAX_BYTES + 1))).toMatchObject({ valid: false, code: 'reason_too_long' });
    expect(validateReason(`nul\u0000char`)).toMatchObject({ valid: false, code: 'reason_invalid_chars' });
    expect(validateReason('bad\ufffe')).toMatchObject({ valid: false, code: 'reason_invalid_chars' });
    expect(validateReason('bad\uffff')).toMatchObject({ valid: false, code: 'reason_invalid_chars' });
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
      container: 'app',
      operationVersion: 'v1',
      artifactRef: 'a1',
      convergenceStrategy: 'REQUIRE_PROMOTION',
      targetLocks: ['b', 'a'],
    });
    const parsed = JSON.parse(json);
    expect(parsed).toEqual({
      releaseDefinitionId: 'def1',
      workloadRef: 'deployments/ns1/api',
      container: 'app',
      operationVersion: 'v1',
      artifactRef: 'a1',
      convergenceStrategy: 'REQUIRE_PROMOTION',
      targetLocks: ['a', 'b'],
    });
    expect(json).not.toContain('idempotencyKey');
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
