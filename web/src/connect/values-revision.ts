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
  CreateValuesRevisionRequestSchema,
  ListSecretsRequestSchema,
  ListValuesRevisionsRequestSchema,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { orchestratorClient } from './client';
import type { SecretOption, SecretRef, ValuesRevision } from '@/types/valuesRevision';
import { mapValuesError } from '@/utils/valuesErrors';
import { statusFromProto } from '@/types/valuesRevision';

function timestampToISO(value: Timestamp | undefined): string | undefined {
  return value ? timestampDate(value).toISOString() : undefined;
}

function mapSecretRef(ref: ProtoSecretRef): SecretRef {
  return { path: ref.path, name: ref.name, key: ref.key, namespace: ref.namespace || undefined };
}

export function mapValuesRevision(revision: ProtoValuesRevision): ValuesRevision {
  return {
    id: revision.id,
    releaseDefinitionId: revision.releaseDefinitionId,
    revision: revision.revision,
    version: revision.version,
    document: new TextDecoder().decode(revision.values),
    valuesDigest: revision.digest,
    status: statusFromProto(revision.status),
    parentRevisionId: revision.parentRevisionId || null,
    secretRefs: revision.secretRefs.map(mapSecretRef),
    createdBy: revision.createdBy,
    createdAt: timestampToISO(revision.createdAt) ?? '',
    approvedBy: revision.approvedBy || undefined,
    approvedAt: timestampToISO(revision.approvedAt),
    rejectedBy: revision.rejectedBy || undefined,
    rejectedAt: timestampToISO(revision.rejectedAt),
    reason: revision.reason || undefined,
  };
}

export async function listValuesRevisions(releaseDefinitionId: string, statusFilter?: ValuesStatus): Promise<ValuesRevision[]> {
  const response = await orchestratorClient.listValuesRevisions(create(ListValuesRevisionsRequestSchema, {
    releaseDefinitionId,
    statusFilter: statusFilter ?? ValuesStatus.UNSPECIFIED,
    limit: 50,
  }));
  return response.revisions.map(mapValuesRevision);
}

export async function getValuesRevision(revisionId: string): Promise<ValuesRevision> {
  const response = await orchestratorClient.getValuesRevision({ revisionId });
  if (!response.revision) throw new ConnectError('revision response is missing', Code.Internal);
  return mapValuesRevision(response.revision);
}

export async function createValuesRevision(input: {
  releaseDefinitionId: string;
  parentRevisionId: string;
  document: string;
  secretRefs: SecretRef[];
  expectedParentVersion: number;
}): Promise<ValuesRevision> {
  const request = create(CreateValuesRevisionRequestSchema, {
    releaseDefinitionId: input.releaseDefinitionId,
    parentRevisionId: input.parentRevisionId,
    document: new TextEncoder().encode(input.document),
    secretRefs: input.secretRefs.map((ref) => create(SecretRefSchema, ref)),
    expectedParentVersion: input.expectedParentVersion,
  });
  const response = await orchestratorClient.createValuesRevision(request);
  if (!response.revision) throw new ConnectError('create response is missing', Code.Internal);
  return mapValuesRevision(response.revision);
}

export async function approveValuesRevision(revisionId: string, expectedVersion: number): Promise<ValuesRevision> {
  const response = await orchestratorClient.approveValuesRevision({ revisionId, expectedVersion });
  if (!response.revision) throw new ConnectError('approve response is missing', Code.Internal);
  return mapValuesRevision(response.revision);
}

export async function rejectValuesRevision(revisionId: string, expectedVersion: number, reason: string): Promise<ValuesRevision> {
  const response = await orchestratorClient.rejectValuesRevision({ revisionId, expectedVersion, reason });
  if (!response.revision) throw new ConnectError('reject response is missing', Code.Internal);
  return mapValuesRevision(response.revision);
}

export async function listSecrets(clusterId: string, releaseDefinitionId: string): Promise<SecretOption[]> {
  const response = await orchestratorClient.listSecrets(create(ListSecretsRequestSchema, { clusterId, releaseDefinitionId }));
  return response.secrets.map((secret) => ({ name: secret.name, keys: [...secret.keys].sort() }));
}

export function valuesError(error: unknown): ReturnType<typeof mapValuesError> {
  return mapValuesError(error);
}
