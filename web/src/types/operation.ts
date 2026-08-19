import type { Timestamp } from '@bufbuild/protobuf/wkt';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import type {
  BundleSummary as ProtoBundleSummary,
  Operation as ProtoOperation,
  OperationSnapshot as ProtoOperationSnapshot,
  TimelineEntry as ProtoTimelineEntry,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { BundleStatus } from '@/gen/common/v1/domain_pb';
import { EmergencyEffectStatus, OperationStatus, TimelineEntryKind } from '@/gen/orchestrator/v1/orchestrator_pb';

export type OperationType = 'INSTALL' | 'UPGRADE' | 'ROLLBACK' | 'EMERGENCY';
export type OperationState = 'pending' | 'preflight' | 'queued' | 'running' | 'cancelling' | 'succeeded' | 'failed' | 'cancelled' | 'timeout';
export type EffectStatus = 'UNSPECIFIED' | 'NOT_STARTED' | 'UNKNOWN' | 'APPLIED' | 'NOT_APPLIED';
export type TimelineEntryKindName = 'STATE_TRANSITION' | 'ACK' | 'ROLLOUT_PROGRESS' | 'ERROR' | 'EMERGENCY_EFFECT_RESOLVED' | 'UNSPECIFIED';
export type PatchOverrideKind = 'LITERAL' | 'SECRET_REF';

export interface PatchOverride {
  path: string;
  value: string;
  kind: PatchOverrideKind;
}

export interface BundleImage {
  ref: string;
  digest: string;
  valuesPath: string;
}

export interface BundleSummary {
  bundleId: string;
  name: string;
  digest: string;
  status: string;
  chartRef: string;
  chartVersion: string;
  chartDigest: string;
  images: BundleImage[];
  createdAt: string | null;
}

export interface Operation {
  operationId: string;
  releaseDefinitionId: string;
  operationType: OperationType;
  state: OperationState;
  /** CAS version; kept as bigint end-to-end to avoid precision loss on int64 wire values. */
  stateVersion: bigint;
  bundleId: string;
  valuesRevisionId: string;
  expectedRevision: number;
  targetRevision: number;
  createdBy: string;
  createdAt: string | null;
  updatedAt: string | null;
  terminalAt: string | null;
  deadline: string | null;
  lastError: string;
  /** Authoritative cluster effect; only meaningful for EMERGENCY operations. */
  effectStatus: EffectStatus;
}

export interface TimelineEntry {
  id: string;
  operationId: string;
  sequence: bigint;
  operationStateVersion: bigint;
  timestamp: string | null;
  kind: TimelineEntryKindName;
  requestId: string;
  errorCode: string;
  errorMessage: string;
  ackStage: string;
  fromState: string;
  toState: string;
  /** EMERGENCY_EFFECT_RESOLVED carries the effect transition (UNKNOWN → APPLIED/NOT_APPLIED). */
  effectFrom: string;
  effectTo: string;
  workloadRef: string;
  ready: number;
  desired: number;
}

export interface OperationSnapshot {
  operation: Operation;
  snapshotSequence: bigint;
  retainedFromSequence: bigint;
}

export interface OperationOptions {
  bundles: BundleSummary[];
}

function timestampToISO(timestamp: Timestamp | undefined): string | null {
  return timestamp ? timestampDate(timestamp).toISOString() : null;
}

function operationType(value: string): OperationType {
  if (value === 'UPGRADE' || value === 'ROLLBACK' || value === 'EMERGENCY') return value;
  return 'INSTALL';
}

function operationState(value: OperationStatus): OperationState {
  switch (value) {
    case OperationStatus.PREFLIGHT:
      return 'preflight';
    case OperationStatus.QUEUED:
      return 'queued';
    case OperationStatus.RUNNING:
      return 'running';
    case OperationStatus.CANCELLING:
      return 'cancelling';
    case OperationStatus.SUCCEEDED:
      return 'succeeded';
    case OperationStatus.FAILED:
      return 'failed';
    case OperationStatus.CANCELLED:
      return 'cancelled';
    case OperationStatus.TIMEOUT:
      return 'timeout';
    default:
      return 'pending';
  }
}

function effectStatus(value: EmergencyEffectStatus): EffectStatus {
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

function timelineEntryKind(value: TimelineEntryKind): TimelineEntryKindName {
  switch (value) {
    case TimelineEntryKind.STATE_TRANSITION:
      return 'STATE_TRANSITION';
    case TimelineEntryKind.ACK:
      return 'ACK';
    case TimelineEntryKind.ROLLOUT_PROGRESS:
      return 'ROLLOUT_PROGRESS';
    case TimelineEntryKind.ERROR:
      return 'ERROR';
    case TimelineEntryKind.EMERGENCY_EFFECT_RESOLVED:
      return 'EMERGENCY_EFFECT_RESOLVED';
    default:
      return 'UNSPECIFIED';
  }
}
function bundleStatus(value: BundleStatus): string {
  switch (value) {
    case BundleStatus.RECEIVED:
      return 'received';
    case BundleStatus.VALIDATED:
      return 'validated';
    case BundleStatus.REJECTED:
      return 'rejected';
    case BundleStatus.ARCHIVED:
      return 'archived';
    default:
      return 'unspecified';
  }
}

export function mapBundleSummary(bundle: ProtoBundleSummary): BundleSummary {
  return {
    bundleId: bundle.id,
    name: bundle.name,
    digest: bundle.digest?.value ?? '',
    status: bundleStatus(bundle.status),
    chartRef: bundle.chartRef,
    chartVersion: bundle.chartVersion,
    chartDigest: bundle.chartDigest,
    images: bundle.images.map((image) => ({ ref: image.ref, digest: image.digest, valuesPath: image.valuesPath })),
    createdAt: timestampToISO(bundle.createdAt),
  };
}

export function mapOperation(operation: ProtoOperation): Operation {
  return {
    operationId: operation.operationId,
    releaseDefinitionId: operation.releaseDefinitionId,
    operationType: operationType(operation.operationType),
    state: operationState(operation.state),
    stateVersion: operation.stateVersion,
    bundleId: operation.bundleId,
    valuesRevisionId: operation.valuesRevisionId,
    expectedRevision: operation.expectedRevision,
    targetRevision: operation.targetRevision,
    createdBy: operation.actor?.userId ?? '',
    createdAt: timestampToISO(operation.createdAt),
    updatedAt: timestampToISO(operation.updatedAt),
    terminalAt: timestampToISO(operation.terminalAt),
    deadline: timestampToISO(operation.deadline),
    lastError: operation.lastError,
    effectStatus: effectStatus(operation.effectStatus),
  };
}

export function mapTimelineEntry(entry: ProtoTimelineEntry): TimelineEntry {
  return {
    id: entry.id,
    operationId: entry.operationId,
    sequence: entry.sequence,
    operationStateVersion: entry.operationStateVersion,
    timestamp: timestampToISO(entry.timestamp),
    kind: timelineEntryKind(entry.kind),
    requestId: entry.requestId,
    errorCode: entry.errorCode,
    errorMessage: entry.errorMessage,
    ackStage: entry.ackStage,
    fromState: entry.fromState,
    toState: entry.toState,
    effectFrom: entry.effectFrom,
    effectTo: entry.effectTo,
    workloadRef: entry.workloadRef,
    ready: entry.ready,
    desired: entry.desired,
  };
}

export function mapOperationSnapshot(snapshot: ProtoOperationSnapshot): OperationSnapshot {
  return {
    operation: mapOperation(snapshot.operation!),
    snapshotSequence: snapshot.snapshotSequence,
    retainedFromSequence: snapshot.retainedFromSequence,
  };
}
