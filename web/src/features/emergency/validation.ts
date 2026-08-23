// Pure validation rules for the Emergency web form and convergence selection.
// All limits come from the REQ-058 input contract and are enforced in UTF-8
// bytes, not JS code points. These are client-side UX guards only — the
// server stays the authoritative validator (ADR-006/ADR-011).
import type { ConvergenceTaskDisplay, EmergencyTargetDisplay } from '@/features/emergency/model';

export const REASON_MIN_BYTES = 1;
export const REASON_MAX_BYTES = 1000;
export const ANNOTATION_MIN_ENTRIES = 1;
export const ANNOTATION_MAX_ENTRIES = 50;
export const ANNOTATION_VALUE_MIN_BYTES = 1;
export const ANNOTATION_VALUE_MAX_BYTES = 2048;
export const MAX_TASKS_PER_REVISION = 50;
export const MAX_CONCURRENT_INTENTS_PER_DEFINITION = 20;

const FORBIDDEN_CODE_POINTS = new Set<number>([0x00, 0xfffe, 0xffff]);

export interface ValidationIssue {
  code: string;
  message: string;
}

export type ValidationResult = { valid: true } | ({ valid: false } & ValidationIssue);

export function utf8ByteLength(value: string): number {
  return new TextEncoder().encode(value).length;
}

function containsForbiddenCodePoints(value: string): boolean {
  for (const char of value) {
    if (FORBIDDEN_CODE_POINTS.has(char.codePointAt(0)!)) return true;
  }
  return false;
}

/** reason: trim 后 1–1000 UTF-8 bytes；禁止 NUL、U+FFFE、U+FFFF（决策 D8）。 */
export function validateReason(raw: string): ValidationResult {
  const reason = raw.trim();
  if (reason.length === 0) {
    return { valid: false, code: 'reason_required', message: '请填写变更原因' };
  }
  const bytes = utf8ByteLength(reason);
  if (bytes > REASON_MAX_BYTES) {
    return {
      valid: false,
      code: 'reason_too_long',
      message: `变更原因需为 1–${REASON_MAX_BYTES} UTF-8 字节（当前 ${bytes} 字节）`,
    };
  }
  if (containsForbiddenCodePoints(reason)) {
    return { valid: false, code: 'reason_invalid_chars', message: '变更原因包含禁止字符' };
  }
  return { valid: true };
}

/** replicas: 整数且 0 ≤ value ≤ max（HPA 管理或超过上限均不可提交）。 */
export function validateReplicas(value: number, max: number, hpaManaged: boolean): ValidationResult {
  if (hpaManaged) {
    return { valid: false, code: 'hpa_managed', message: '副本数由 HPA 管理，不可修改' };
  }
  if (!Number.isInteger(value)) {
    return { valid: false, code: 'invalid_replicas', message: '副本数必须为整数' };
  }
  if (value < 0 || value > max) {
    return { valid: false, code: 'invalid_replicas', message: `副本数需在 0–${max} 之间` };
  }
  return { valid: true };
}

export interface AnnotationEntryDraft {
  localId: string;
  key: string;
  value: string;
  scope: string;
}

/** annotations: 1–50 条、key 唯一、value 1–2048 UTF-8 bytes、同 scope。 */
export function validateAnnotationEntries(entries: AnnotationEntryDraft[]): ValidationResult {
  if (entries.length < ANNOTATION_MIN_ENTRIES || entries.length > ANNOTATION_MAX_ENTRIES) {
    return {
      valid: false,
      code: 'invalid_annotation_entries',
      message: `注解需为 ${ANNOTATION_MIN_ENTRIES}–${ANNOTATION_MAX_ENTRIES} 条（当前 ${entries.length} 条）`,
    };
  }
  const seenKeys = new Set<string>();
  const firstScope = entries[0]?.scope;
  for (const entry of entries) {
    if (entry.key.trim() === '') {
      return { valid: false, code: 'annotation_key_not_allowed', message: '注解 key 不能为空' };
    }
    if (seenKeys.has(entry.key)) {
      return { valid: false, code: 'duplicate_annotation_key', message: `注解 key "${entry.key}" 重复` };
    }
    seenKeys.add(entry.key);
    if (entry.scope !== firstScope) {
      return { valid: false, code: 'annotation_scope_mismatch', message: '同一次操作的所有注解必须为同一 scope' };
    }
    const bytes = utf8ByteLength(entry.value);
    if (bytes < ANNOTATION_VALUE_MIN_BYTES || bytes > ANNOTATION_VALUE_MAX_BYTES) {
      return {
        valid: false,
        code: 'invalid_annotation_entries',
        message: `注解 value 需为 ${ANNOTATION_VALUE_MIN_BYTES}–${ANNOTATION_VALUE_MAX_BYTES} UTF-8 字节（key "${entry.key}" 当前 ${bytes} 字节）`,
      };
    }
    if (containsForbiddenCodePoints(entry.value)) {
      return { valid: false, code: 'invalid_annotation_entries', message: `注解 value "${entry.key}" 包含禁止字符` };
    }
  }
  return { valid: true };
}

/**
 * Mapping completeness (AC-058-14): REQUIRE_PROMOTION is selectable only when
 * every affected field of the pending intent carries a unique promotion
 * mapping on the target. Image mappings are keyed by field "image_digest" +
 * container, replicas by field "replicas", annotations by field == key.
 */
export function imageMappingComplete(
  target: EmergencyTargetDisplay,
  container: string,
): boolean {
  return target.promotions.some(
    (mapping) =>
      mapping.workloadKind === target.workloadRef.kind &&
      mapping.workloadName === target.workloadRef.name &&
      mapping.container === container &&
      mapping.field === 'image_digest' &&
      mapping.valuesPath !== '',
  );
}

export function replicasMappingComplete(target: EmergencyTargetDisplay): boolean {
  return target.promotions.some(
    (mapping) =>
      mapping.workloadKind === target.workloadRef.kind &&
      mapping.workloadName === target.workloadRef.name &&
      mapping.container === '' &&
      mapping.field === 'replicas' &&
      mapping.valuesPath !== '',
  );
}

export function annotationMappingComplete(target: EmergencyTargetDisplay, keys: string[]): boolean {
  return keys.every((key) =>
    target.promotions.some(
      (mapping) =>
        mapping.workloadKind === target.workloadRef.kind &&
        mapping.workloadName === target.workloadRef.name &&
        mapping.container === '' &&
        mapping.field === key &&
        mapping.valuesPath !== '',
    ),
  );
}

/** Canonical stable JSON of the intent fields; the store hashes it to detect
 * intent changes (AC-058-17). The idempotency key is deliberately excluded —
 * it stays stable across retries of the same frozen intent. */
export interface EmergencyIntentFields {
  releaseDefinitionId: string;
  workloadRef: string;
  container: string;
  operationVersion: string;
  artifactRef: string;
  convergenceStrategy: string;
  targetLocks: string[];
}

export function canonicalIntentJson(fields: EmergencyIntentFields): string {
  return JSON.stringify({
    releaseDefinitionId: fields.releaseDefinitionId,
    workloadRef: fields.workloadRef,
    container: fields.container,
    operationVersion: fields.operationVersion,
    artifactRef: fields.artifactRef,
    convergenceStrategy: fields.convergenceStrategy,
    targetLocks: [...fields.targetLocks].sort(),
  });
}

export interface SelectionConflict {
  taskId: string;
  reason: string;
}

export interface ConvergenceSelectionResult {
  valid: boolean;
  conflicts: SelectionConflict[];
}

/**
 * Client-side compatibility pre-check (AC-058-34): 1–50 tasks, no task bound
 * to an active revision, no overlapping promotion paths. The Prepare RPC stays
 * the final authority — this is UX pre-judgement only.
 */
export function validateConvergenceSelection(tasks: ConvergenceTaskDisplay[]): ConvergenceSelectionResult {
  const conflicts: SelectionConflict[] = [];
  if (tasks.length < 1 || tasks.length > MAX_TASKS_PER_REVISION) {
    return {
      valid: false,
      conflicts: [
        {
          taskId: '',
          reason: `需选择 1–${MAX_TASKS_PER_REVISION} 个任务（当前 ${tasks.length} 个）`,
        },
      ],
    };
  }
  const pathOwner = new Map<string, string>();
  for (const task of tasks) {
    if (task.activeRevisionId) {
      conflicts.push({ taskId: task.taskId, reason: `任务已绑定 revision ${task.activeRevisionId}` });
      continue;
    }
    for (const path of task.promotionPaths) {
      const owner = pathOwner.get(path);
      if (owner) {
        conflicts.push({
          taskId: task.taskId,
          reason: `收敛路径 "${path}" 与任务 ${owner} 重叠`,
        });
        continue;
      }
      pathOwner.set(path, task.taskId);
    }
  }
  return { valid: conflicts.length === 0, conflicts };
}
