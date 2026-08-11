import type { Timestamp } from '@bufbuild/protobuf/wkt';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import type {
  BundleSummary as ProtoBundleSummary,
  Operation as ProtoOperation,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { BundleStatus } from '@/gen/common/v1/domain_pb';
import { OperationStatus } from '@/gen/orchestrator/v1/orchestrator_pb';

export type OperationType = 'INSTALL' | 'UPGRADE' | 'ROLLBACK';
export type OperationState = 'pending' | 'preflight' | 'queued' | 'running' | 'cancelling' | 'succeeded' | 'failed' | 'cancelled' | 'timeout';
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
  stateVersion: number;
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
}

export interface OperationOptions {
  bundles: BundleSummary[];
}

function timestampToISO(timestamp: Timestamp | undefined): string | null {
  return timestamp ? timestampDate(timestamp).toISOString() : null;
}

function operationType(value: string): OperationType {
  if (value === 'UPGRADE' || value === 'ROLLBACK') return value;
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
    stateVersion: Number(operation.stateVersion),
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
  };
}
