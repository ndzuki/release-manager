// Emergency + authorization Connect wrapper (plan v3 Step 2/3 seam).
// Follows the operation-api.ts pattern: module-level client injection for
// tests (setEmergencyClientForTest), per-call AbortSignal, and typed error
// mapping delegated to features/emergency/errors.ts.
//
// Contract divergence note (recorded in the TASK implementation record):
// the canonical ExecuteEmergencyChangeRequest carries the idempotency key in
// the request body (field idempotency_key), not in the HTTP Idempotency-Key
// header — per the merged TASK-079 contract.
import { create } from '@bufbuild/protobuf';
import { createClient, type Client } from '@connectrpc/connect';
import {
  AuthorizationService,
  GetAuthorizationSnapshotRequestSchema,
} from '@/gen/auth/v1/auth_pb';
import {
  CheckEmergencyConflictRequestSchema,
  ConvergenceStrategy,
  CreatePrepareSessionRequestSchema,
  ExecuteEmergencyChangeRequestSchema,
  GetOperationRequestSchema,
  GetPrepareSessionRequestSchema,
  ListCandidateArtifactsRequestSchema,
  ListConvergenceTasksRequestSchema,
  ListEmergencyTargetsRequestSchema,
  OrchestratorService,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { orchestratorClient, transport } from '@/connect/client';
import {
  mapCandidateArtifact,
  mapConvergenceTask,
  mapEmergencyResult,
  mapEmergencyTarget,
  type CandidateArtifactDisplay,
  type ConvergenceTaskDisplay,
  type EmergencyResultDisplay,
  type EmergencyTargetDisplay,
} from '@/features/emergency/model';

export interface EmergencyAuthorizationProjection {
  organizationId: string;
  customerId: string;
  bindingActive: boolean;
  customerActive: boolean;
  role: string;
  canExecuteEmergency: boolean;
  canResolveEmergency: boolean;
  canCreateValuesRevision: boolean;
  canApproveValuesRevision: boolean;
  sourceVersion: bigint;
  policyVersion: bigint;
  checkpoint: bigint;
  fresh: boolean;
  actorId: string;
  /** Global kill switch; a missing value fails closed to false (D1). */
  emergencyChangeEnabled: boolean;
}

export interface EmergencyConflictDisplay {
  hasConflict: boolean;
  runningOperation: {
    operationId: string;
    type: string;
    status: string;
    startedAt: string | null;
  } | null;
}

export interface ExecuteEmergencyInput {
  releaseDefinitionId: string;
  /** Canonical "<gvr.resource>/<namespace>/<name>" string form. */
  workloadRef: string;
  container: string;
  operationVersion: string;
  artifactRef: string;
  convergenceStrategy: 'REQUIRE_PROMOTION' | 'REVERT_ON_NEXT_RECONCILE';
  targetLocks: string[];
  idempotencyKey: string;
}

export interface ExecuteEmergencyOutput {
  operationId: string;
  operationVersion: string;
  result: EmergencyResultDisplay | null;
}

export interface PrepareSessionDisplay {
  prepareToken: string;
  expiresAt: string | null;
  parentRevisionId: string;
  parentVersion: bigint;
  lockedPaths: string[];
  conflictTaskIds: string[];
}

export interface PreparedConvergenceDisplay {
  releaseDefinitionId: string;
  parentRevisionId: string;
  document: string;
  lockedPaths: string[];
  expiresAt: string | null;
  taskIds: string[];
  lockedPathsHash: string;
  parentVersion: bigint;
}

let orchestratorEmergencyClient: Client<typeof OrchestratorService> = orchestratorClient;
let authorizationClient: Client<typeof AuthorizationService> = createClient(AuthorizationService, transport);

export function setEmergencyClientForTest(
  nextClient: Client<typeof OrchestratorService>,
  nextAuthzClient?: Client<typeof AuthorizationService>,
): void {
  orchestratorEmergencyClient = nextClient;
  if (nextAuthzClient) authorizationClient = nextAuthzClient;
}

export async function getAuthorizationSnapshot(
  organizationId: string,
  customerId: string,
  signal?: AbortSignal,
): Promise<EmergencyAuthorizationProjection> {
  const response = await authorizationClient.getAuthorizationSnapshot(
    create(GetAuthorizationSnapshotRequestSchema, { organizationId, customerId }),
    { signal },
  );
  return {
    organizationId: response.organizationId,
    customerId: response.customerId,
    bindingActive: response.bindingActive,
    customerActive: response.customerActive,
    role: response.role,
    canExecuteEmergency: response.canExecuteEmergency,
    canResolveEmergency: response.canResolveEmergency,
    canCreateValuesRevision: response.canCreateValuesRevision,
    canApproveValuesRevision: response.canApproveValuesRevision,
    sourceVersion: response.sourceVersion,
    policyVersion: response.policyVersion,
    checkpoint: response.checkpoint,
    fresh: response.fresh,
    actorId: response.actorId,
    emergencyChangeEnabled: response.emergencyChangeEnabled,
  };
}

export async function checkEmergencyConflict(
  releaseDefinitionId: string,
  signal?: AbortSignal,
): Promise<EmergencyConflictDisplay> {
  const response = await orchestratorEmergencyClient.checkEmergencyConflict(
    create(CheckEmergencyConflictRequestSchema, { releaseDefinitionId }),
    { signal },
  );
  const running = response.runningOperation;
  return {
    hasConflict: response.hasConflict,
    runningOperation: running
      ? {
          operationId: running.operationId,
          type: running.type,
          status: running.status,
          startedAt: running.startedAt ? new Date(Number(running.startedAt.seconds) * 1000).toISOString() : null,
        }
      : null,
  };
}

export async function listEmergencyTargets(
  releaseDefinitionId: string,
  signal?: AbortSignal,
): Promise<EmergencyTargetDisplay[]> {
  const response = await orchestratorEmergencyClient.listEmergencyTargets(
    create(ListEmergencyTargetsRequestSchema, { releaseDefinitionId }),
    { signal },
  );
  return response.targets.map(mapEmergencyTarget);
}

export async function listCandidateArtifacts(
  input: {
    organizationId: string;
    releaseDefinitionId: string;
    workloadRef: string;
    container: string;
    operationVersion: string;
  },
  signal?: AbortSignal,
): Promise<CandidateArtifactDisplay[]> {
  const response = await orchestratorEmergencyClient.listCandidateArtifacts(
    create(ListCandidateArtifactsRequestSchema, {
      organizationId: input.organizationId,
      releaseDefinitionId: input.releaseDefinitionId,
      workloadRef: input.workloadRef,
      container: input.container,
      operationVersion: input.operationVersion,
    }),
    { signal },
  );
  return response.artifacts.map(mapCandidateArtifact);
}

/**
 * Fetches the authoritative EmergencyResult projection attached to
 * GetOperation (AC-058-20: a direct URL restores the full result without any
 * in-memory dependency on the submitting page).
 */
export async function getEmergencyResult(
  operationId: string,
  signal?: AbortSignal,
): Promise<EmergencyResultDisplay | null> {
  const response = await orchestratorEmergencyClient.getOperation(
    create(GetOperationRequestSchema, { operationId }),
    { signal },
  );
  return mapEmergencyResult(response.emergencyResult);
}

export async function executeEmergencyChange(
  input: ExecuteEmergencyInput,
  signal?: AbortSignal,
): Promise<ExecuteEmergencyOutput> {  const response = await orchestratorEmergencyClient.executeEmergencyChange(
    create(ExecuteEmergencyChangeRequestSchema, {
      releaseDefinitionId: input.releaseDefinitionId,
      workloadRef: input.workloadRef,
      container: input.container,
      operationVersion: input.operationVersion,
      artifactRef: input.artifactRef,
      convergenceStrategy:
        input.convergenceStrategy === 'REQUIRE_PROMOTION'
          ? ConvergenceStrategy.REQUIRE_PROMOTION
          : ConvergenceStrategy.REVERT_ON_NEXT_RECONCILE,
      targetLocks: input.targetLocks,
      idempotencyKey: input.idempotencyKey,
    }),
    { signal },
  );
  return {
    operationId: response.operationId,
    operationVersion: response.operationVersion,
    result: mapEmergencyResult(response.result),
  };
}

export async function listConvergenceTasks(
  releaseDefinitionId: string,
  signal?: AbortSignal,
): Promise<ConvergenceTaskDisplay[]> {
  const response = await orchestratorEmergencyClient.listConvergenceTasks(
    create(ListConvergenceTasksRequestSchema, {
      releaseDefinitionId,
      statusFilter: 'PENDING_PROMOTION',
    }),
    { signal },
  );
  return response.tasks.map(mapConvergenceTask);
}

export async function createPrepareSession(
  input: { releaseDefinitionId: string; taskIds: string[]; expectedParentVersion: bigint },
  signal?: AbortSignal,
): Promise<PrepareSessionDisplay> {
  const response = await orchestratorEmergencyClient.createPrepareSession(
    create(CreatePrepareSessionRequestSchema, {
      releaseDefinitionId: input.releaseDefinitionId,
      taskIds: input.taskIds,
      expectedParentVersion: input.expectedParentVersion,
    }),
    { signal },
  );
  return {
    prepareToken: response.prepareToken,
    expiresAt: response.expiresAt ? new Date(Number(response.expiresAt.seconds) * 1000).toISOString() : null,
    parentRevisionId: response.parentRevisionId,
    parentVersion: response.parentVersion,
    lockedPaths: [...response.lockedPaths],
    conflictTaskIds: [...response.conflictTaskIds],
  };
}

export async function getPrepareSession(
  prepareToken: string,
  signal?: AbortSignal,
): Promise<PreparedConvergenceDisplay> {
  const response = await orchestratorEmergencyClient.getPrepareSession(
    create(GetPrepareSessionRequestSchema, { prepareToken }),
    { signal },
  );
  return {
    releaseDefinitionId: response.releaseDefinitionId,
    parentRevisionId: response.parentRevisionId,
    document: response.document,
    lockedPaths: [...response.lockedPaths],
    expiresAt: response.expiresAt ? new Date(Number(response.expiresAt.seconds) * 1000).toISOString() : null,
    taskIds: [...response.taskIds],
    lockedPathsHash: response.lockedPathsHash,
    parentVersion: response.parentVersion,
  };
}
