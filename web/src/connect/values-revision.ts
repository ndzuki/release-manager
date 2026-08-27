import { Code, ConnectError } from '@connectrpc/connect';
import { create } from '@bufbuild/protobuf';
import { timestampDate, type Timestamp } from '@bufbuild/protobuf/wkt';
import {
  SecretRefSchema,
  ValuesStatus,
  type SecretRef as ProtoSecretRef,
  type ValuesRevision as ProtoValuesRevision,
} from '@/gen/common/v1/domain_pb';
import {
  ApproveValuesRevisionRequestSchema,
  CreateValuesRevisionRequestSchema,
  DiscardValuesRevisionRequestSchema,
  ListSecretsRequestSchema,
  ListValuesRevisionsRequestSchema,
  RejectValuesRevisionRequestSchema,
  SubmitValuesRevisionRequestSchema,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { orchestratorClient } from './client';
import type { SecretOption, SecretRef, ValuesRevision } from '@/types/valuesRevision';
import { mapValuesError } from '@/utils/valuesErrors';
import { statusFromProto } from '@/types/valuesRevision';

function timestampToISO(value: Timestamp | undefined): string | undefined {
  return value ? timestampDate(value).toISOString() : undefined;
}

function mapSecretRef(ref: ProtoSecretRef): SecretRef {
  return { path: ref.path, name: ref.name, key: ref.key };
}

export function mapValuesRevision(revision: ProtoValuesRevision): ValuesRevision {
  return {
    id: revision.id,
    releaseDefinitionId: revision.releaseDefinitionId,
    revision: Number(revision.version),
    stateVersion: revision.stateVersion.toString(),
    document: new TextDecoder().decode(revision.canonicalDocument),
    valuesDigest: revision.digest,
    status: statusFromProto(revision.status),
    parentRevisionId: revision.parentRevisionId || null,
    secretRefs: revision.secretRefs.map(mapSecretRef),
    createdByUserId: revision.createdByUserId,
    createdAt: timestampToISO(revision.createdAt) ?? '',
    submittedAt: timestampToISO(revision.submittedAt),
    decidedAt: timestampToISO(revision.decidedAt),
    convergenceTaskIds: [...revision.convergenceTaskIds],
    lockedPaths: [...revision.lockedPaths],
  };
}

export async function listValuesRevisions(releaseDefinitionId: string, statusFilter?: ValuesStatus): Promise<ValuesRevision[]> {
  const response = await orchestratorClient.listValuesRevisions(create(ListValuesRevisionsRequestSchema, {
    releaseDefinitionId,
    status: statusFilter ?? ValuesStatus.UNSPECIFIED,
    pageSize: 50,
  }));
  return response.items.map(mapValuesRevision);
}

export async function getValuesRevision(revisionId: string): Promise<ValuesRevision> {
  const response = await orchestratorClient.getValuesRevision({ revisionId });
  return mapValuesRevision(response);
}

export async function createValuesRevision(input: {
  releaseDefinitionId: string;
  parentRevisionId: string;
  document: string;
  secretRefs: SecretRef[];
  expectedParentVersion: number;
  /** Convergence mode: single-use Prepare Session token (REQ-058/068). */
  prepareToken?: string;
}): Promise<ValuesRevision> {
  const request = create(CreateValuesRevisionRequestSchema, {
    releaseDefinitionId: input.releaseDefinitionId,
    parentRevisionId: input.parentRevisionId,
    document: input.document,
    secretRefs: input.secretRefs.map((ref) => create(SecretRefSchema, ref)),
    expectedParentVersion: BigInt(input.expectedParentVersion),
    prepareToken: input.prepareToken ?? '',
  });
  const response = await orchestratorClient.createValuesRevision(request);
  if (!response.revision) throw new ConnectError('create response is missing', Code.Internal);
  return mapValuesRevision(response.revision);
}

/** Approval chain wrappers (plan v3 Step 8 sub-goal: TASK-055 marked done but
 * the web approval chain was never wired — the canonical RPCs exist in gen). */
function mapDecisionResponse(response: { revision?: ProtoValuesRevision }): ValuesRevision {
  if (!response.revision) throw new ConnectError('decision response is missing', Code.Internal);
  return mapValuesRevision(response.revision);
}

export async function submitValuesRevision(revisionId: string, expectedStateVersion: string, comment = ''): Promise<ValuesRevision> {
  const response = await orchestratorClient.submitValuesRevision(
    create(SubmitValuesRevisionRequestSchema, {
      revisionId,
      expectedStateVersion: BigInt(expectedStateVersion),
      comment,
    }),
  );
  return mapDecisionResponse(response);
}

export async function approveValuesRevision(revisionId: string, expectedStateVersion: string, comment = ''): Promise<ValuesRevision> {
  const response = await orchestratorClient.approveValuesRevision(
    create(ApproveValuesRevisionRequestSchema, {
      revisionId,
      expectedStateVersion: BigInt(expectedStateVersion),
      comment,
    }),
  );
  return mapDecisionResponse(response);
}

export async function rejectValuesRevision(revisionId: string, expectedStateVersion: string, reason = ''): Promise<ValuesRevision> {
  const response = await orchestratorClient.rejectValuesRevision(
    create(RejectValuesRevisionRequestSchema, {
      revisionId,
      expectedStateVersion: BigInt(expectedStateVersion),
      reason,
    }),
  );
  return mapDecisionResponse(response);
}

export async function discardValuesRevision(revisionId: string, expectedStateVersion: string, comment = ''): Promise<ValuesRevision> {
  const response = await orchestratorClient.discardValuesRevision(
    create(DiscardValuesRevisionRequestSchema, {
      revisionId,
      expectedStateVersion: BigInt(expectedStateVersion),
      comment,
    }),
  );
  return mapDecisionResponse(response);
}

export async function listSecrets(clusterId: string, releaseDefinitionId: string): Promise<SecretOption[]> {
  const response = await orchestratorClient.listSecrets(create(ListSecretsRequestSchema, { clusterId, releaseDefinitionId }));
  return response.secrets.map((secret) => ({ name: secret.name, keys: [...secret.keys].sort() }));
}

export function valuesError(error: unknown): ReturnType<typeof mapValuesError> {
  return mapValuesError(error);
}
