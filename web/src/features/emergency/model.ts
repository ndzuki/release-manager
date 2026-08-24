// Emergency display models mapped one-way from the generated Connect
// messages (per ADR-002: the generated client is the only protocol source).
//
// Contract divergences vs REQ-058 spec are recorded in the TASK implementation
// record; this module maps what upstream/main actually delivers:
// - workload_ref on Execute is the "<gvr.resource>/<namespace>/<name>" string
//   form (not a structured WorkloadRef); ListEmergencyTargets returns the
//   structured WorkloadRef message.
// - EmergencyResult.requested is a bool ("accepted and queued"), not a typed
//   requested-values union; typed evidence lives in before/after.
// - ConvergenceTaskDetail exposes reason (server-sanitized) and
//   selectable/incompatibility_reason computed by the server.
import { timestampDate } from '@bufbuild/protobuf/wkt';
import type { Timestamp } from '@bufbuild/protobuf/wkt';
import type {
  CandidateArtifactSummary as ProtoCandidateArtifactSummary,
  ConvergenceTaskDetail as ProtoConvergenceTaskDetail,
  EmergencyResult as ProtoEmergencyResult,
  EmergencyTarget as ProtoEmergencyTarget,
  EmergencyTypedValues as ProtoEmergencyTypedValues,
  PromotionMapping as ProtoPromotionMapping,
  RunningOperationDetail as ProtoRunningOperationDetail,
  WorkloadRef as ProtoWorkloadRef,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import {
  EmergencyAction,
  EmergencyConvergence,
  EmergencyEffectStatus,
} from '@/gen/orchestrator/v1/orchestrator_pb';

export type EmergencyOpType =
  | 'SET_CONTAINER_IMAGE'
  | 'SET_REPLICAS'
  | 'SET_APPROVED_ANNOTATION'
  | 'UNSPECIFIED';

export type ConvergencePolicy = 'REQUIRE_PROMOTION' | 'REVERT_ON_NEXT_RECONCILE' | 'UNSPECIFIED';

export type EmergencyEffectStatusName = 'NOT_STARTED' | 'UNKNOWN' | 'APPLIED' | 'NOT_APPLIED' | 'UNSPECIFIED';

export interface WorkloadRefDisplay {
  kind: string;
  namespace: string;
  name: string;
  uid: string;
}

export interface PromotionMappingDisplay {
  workloadKind: string;
  workloadName: string;
  container: string;
  field: string;
  valuesPath: string;
}

export type ActionAvailabilityReason = 'hpa_managed' | 'unsupported_operation';

export interface ActionAvailabilityDisplay {
  available: boolean;
  reasonCode?: ActionAvailabilityReason;
}

export interface EmergencyImageActionDisplay {
  container: string;
  currentImageRef: string;
  availability: ActionAvailabilityDisplay;
  promotions: PromotionMappingDisplay[];
}

export interface EmergencyReplicasActionDisplay {
  currentReplicas: number;
  maxEmergencyReplicas: number;
  hpaManaged: boolean;
  availability: ActionAvailabilityDisplay;
  promotions: PromotionMappingDisplay[];
}

export interface EmergencyAnnotationActionDisplay {
  key: string;
  currentValue: string;
  availability: ActionAvailabilityDisplay;
  promotions: PromotionMappingDisplay[];
}

export interface EmergencyTargetDisplay {
  workloadRef: WorkloadRefDisplay;
  containers: string[];
  supportedOperations: EmergencyOpType[];
  promotions: PromotionMappingDisplay[];
  imageActions: EmergencyImageActionDisplay[];
  replicasAction: EmergencyReplicasActionDisplay | null;
  annotationActions: EmergencyAnnotationActionDisplay[];
}

export interface CandidateArtifactDisplay {
  id: string;
  repository: string;
  digest: string;
  ref: string;
  validatedAt: string | null;
  sourceId: string;
}

export interface RunningOperationDisplay {
  operationId: string;
  type: string;
  status: string;
  startedAt: string | null;
}

export type EmergencyTypedValuesDisplay =
  | { case: 'image'; container: string; imageReference: string }
  | { case: 'replicas'; replicas: number }
  | { case: 'annotations'; annotations: Array<{ key: string; value: string }> }
  | { case: 'empty' };

export interface ConvergenceTaskSummaryDisplay {
  taskId: string;
  status: string;
}

export interface EmergencyResultDisplay {
  opType: EmergencyOpType;
  convergencePolicy: ConvergencePolicy;
  requested: boolean;
  before: EmergencyTypedValuesDisplay | null;
  after: EmergencyTypedValuesDisplay | null;
  effectStatus: EmergencyEffectStatusName;
  convergenceTasks: ConvergenceTaskSummaryDisplay[];
  revertStatus: string;
  reconciledByOperationId: string;
}

export interface ConvergenceTaskDisplay {
  taskId: string;
  operationId: string;
  opType: EmergencyOpType;
  targetSummary: string;
  submittedAt: string | null;
  reasonDisplay: string;
  promotionPaths: string[];
  activeRevisionId: string;
  activeRevisionStatus: string;
  selectable: boolean;
  incompatibilityReason: string;
}

// Workload kind → operator control-stream GVR resource (per
// core/go/connect-rpc.md and the backend workloadGVRResources map in
// internal/orchestrator/emergency.go).
const WORKLOAD_KIND_GVR: Record<string, string> = {
  DEPLOYMENT: 'deployments',
  STATEFUL_SET: 'statefulsets',
  DAEMON_SET: 'daemonsets',
};

function timestampToISO(timestamp: Timestamp | undefined): string | null {
  return timestamp ? timestampDate(timestamp).toISOString() : null;
}

export function mapOpType(value: EmergencyAction): EmergencyOpType {
  switch (value) {
    case EmergencyAction.SET_CONTAINER_IMAGE:
      return 'SET_CONTAINER_IMAGE';
    case EmergencyAction.SET_REPLICAS:
      return 'SET_REPLICAS';
    case EmergencyAction.SET_APPROVED_ANNOTATION:
      return 'SET_APPROVED_ANNOTATION';
    default:
      return 'UNSPECIFIED';
  }
}

export function mapConvergencePolicy(value: EmergencyConvergence): ConvergencePolicy {
  switch (value) {
    case EmergencyConvergence.REQUIRE_PROMOTION:
      return 'REQUIRE_PROMOTION';
    case EmergencyConvergence.REVERT_ON_NEXT_RECONCILE:
      return 'REVERT_ON_NEXT_RECONCILE';
    default:
      return 'UNSPECIFIED';
  }
}

export function mapEffectStatus(value: EmergencyEffectStatus): EmergencyEffectStatusName {
  switch (value) {
    case EmergencyEffectStatus.NOT_STARTED:
      return 'NOT_STARTED';
    case EmergencyEffectStatus.UNKNOWN:
      return 'UNKNOWN';
    case EmergencyEffectStatus.APPLIED:
      return 'APPLIED';
    case EmergencyEffectStatus.NOT_APPLIED:
      return 'NOT_APPLIED';
    default:
      return 'UNSPECIFIED';
  }
}

export function mapWorkloadRef(ref: ProtoWorkloadRef | undefined): WorkloadRefDisplay {
  return { kind: ref?.kind ?? '', namespace: ref?.namespace ?? '', name: ref?.name ?? '', uid: ref?.uid ?? '' };
}

/**
 * Serializes a structured WorkloadRef into the canonical
 * "<gvr.resource>/<namespace>/<name>" wire form used by Execute and
 * ListCandidateArtifacts.
 */
export function workloadRefToWire(ref: WorkloadRefDisplay): string {
  const resource = WORKLOAD_KIND_GVR[ref.kind] ?? ref.kind.toLowerCase();
  return `${resource}/${ref.namespace}/${ref.name}`;
}

export function mapPromotionMapping(mapping: ProtoPromotionMapping): PromotionMappingDisplay {
  return {
    workloadKind: mapping.workloadKind,
    workloadName: mapping.workloadName,
    container: mapping.container,
    field: mapping.field,
    valuesPath: mapping.valuesPath,
  };
}

function promotionFor(
  mappings: PromotionMappingDisplay[],
  workload: WorkloadRefDisplay,
  container: string,
  field: string,
): PromotionMappingDisplay[] {
  return mappings.filter(
    (mapping) =>
      mapping.workloadKind === workload.kind &&
      mapping.workloadName === workload.name &&
      mapping.container === container &&
      mapping.field === field &&
      mapping.valuesPath !== '',
  );
}

export function mapEmergencyTarget(target: ProtoEmergencyTarget): EmergencyTargetDisplay {
  const workloadRef = mapWorkloadRef(target.workloadRef);
  const supportedOperations = target.supportedOperations.map(mapOpType);
  const promotions = target.promotions.map(mapPromotionMapping);

  const imageSupported = supportedOperations.includes('SET_CONTAINER_IMAGE');
  const imageActions: EmergencyImageActionDisplay[] = target.containers.map((container) => ({
    container,
    currentImageRef: target.currentImageRefs[container] ?? '',
    availability: { available: imageSupported },
    promotions: promotionFor(promotions, workloadRef, container, 'image_digest'),
  }));

  const replicasSupported = supportedOperations.includes('SET_REPLICAS');
  const replicasAction: EmergencyReplicasActionDisplay | null = {
    currentReplicas: target.currentReplicas,
    maxEmergencyReplicas: target.maxEmergencyReplicas,
    hpaManaged: target.hpaManaged,
    availability: replicasSupported
      ? target.hpaManaged
        ? { available: false, reasonCode: 'hpa_managed' }
        : { available: true }
      : { available: false, reasonCode: 'unsupported_operation' },
    promotions: promotionFor(promotions, workloadRef, '', 'replicas'),
  };

  const annotationSupported = supportedOperations.includes('SET_APPROVED_ANNOTATION');
  const annotationActions: EmergencyAnnotationActionDisplay[] = Object.entries(
    target.currentAnnotations,
  ).map(([key, currentValue]) => ({
    key,
    currentValue,
    availability: annotationSupported
      ? { available: true }
      : { available: false, reasonCode: 'unsupported_operation' },
    promotions: promotionFor(promotions, workloadRef, '', key),
  }));

  return {
    workloadRef,
    containers: [...target.containers],
    supportedOperations,
    promotions,
    imageActions,
    replicasAction,
    annotationActions,
  };
}

export function mapCandidateArtifact(artifact: ProtoCandidateArtifactSummary): CandidateArtifactDisplay {
  return {
    id: artifact.id,
    repository: artifact.repository,
    digest: artifact.digest,
    ref: artifact.ref,
    validatedAt: timestampToISO(artifact.validatedAt),
    sourceId: artifact.sourceId,
  };
}

export function mapRunningOperation(detail: ProtoRunningOperationDetail | undefined): RunningOperationDisplay | null {
  if (!detail) return null;
  return {
    operationId: detail.operationId,
    type: detail.type,
    status: detail.status,
    startedAt: timestampToISO(detail.startedAt),
  };
}

export function mapEmergencyTypedValues(values: ProtoEmergencyTypedValues | undefined): EmergencyTypedValuesDisplay | null {
  if (!values) return null;
  switch (values.values.case) {
    case 'imageRefValues':
      return {
        case: 'image',
        container: values.values.value.container,
        imageReference: values.values.value.imageReference,
      };
    case 'replicasValues':
      return { case: 'replicas', replicas: values.values.value.replicas };
    case 'annotationValues':
      return {
        case: 'annotations',
        annotations: values.values.value.annotations.map((entry) => ({ key: entry.key, value: entry.value })),
      };
    default:
      return { case: 'empty' };
  }
}

export function mapEmergencyResult(result: ProtoEmergencyResult | undefined): EmergencyResultDisplay | null {
  if (!result) return null;
  return {
    opType: mapOpType(result.opType),
    convergencePolicy: mapConvergencePolicy(result.convergencePolicy),
    requested: result.requested,
    before: mapEmergencyTypedValues(result.before),
    after: mapEmergencyTypedValues(result.after),
    effectStatus: mapEffectStatus(result.effectStatus),
    convergenceTasks: result.convergenceTasks.map((task) => ({
      taskId: task.taskId,
      status: task.status,
    })),
    revertStatus: result.revertStatus,
    reconciledByOperationId: result.reconciledByOperationId,
  };
}

export function mapConvergenceTask(task: ProtoConvergenceTaskDetail): ConvergenceTaskDisplay {
  return {
    taskId: task.taskId,
    operationId: task.operationId,
    opType: mapOpType(task.opType),
    targetSummary: task.targetSummary,
    submittedAt: timestampToISO(task.submittedAt),
    reasonDisplay: task.reason,
    promotionPaths: [...task.promotionPaths],
    activeRevisionId: task.activeRevisionId,
    activeRevisionStatus: task.activeRevisionStatus,
    selectable: task.selectable,
    incompatibilityReason: task.incompatibilityReason,
  };
}
