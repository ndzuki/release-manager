import { Code, ConnectError, createClient } from '@connectrpc/connect';
import type { Client } from '@connectrpc/connect';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import { ClusterStatus } from '@/gen/common/v1/domain_pb';
import type { Cluster as ProtoCluster } from '@/gen/common/v1/domain_pb';
import {
  ArtifactMode,
  ArtifactType,
  OrchestratorService,
  RouteValidationDetailSchema,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import type { ClusterRoute as ProtoRoute } from '@/gen/orchestrator/v1/orchestrator_pb';
import { transport } from '@/connect/client';
import type {
  Cluster,
  ClusterFormInput,
  ClusterSummary,
  RouteRuleInput,
  SaveError,
} from '@/types/cluster';

let client: Client<typeof OrchestratorService> = createClient(OrchestratorService, transport);

export function setClusterClientForTest(replacement: Client<typeof OrchestratorService>) {
  client = replacement;
}

function mapMode(mode: ArtifactMode): RouteRuleInput['mode'] {
  if (mode === ArtifactMode.PULL_THROUGH_CACHE) return 'pull_through_cache';
  if (mode === ArtifactMode.REPLICATED) return 'replicated';
  return 'direct';
}

function mapArtifactType(type: ArtifactType): RouteRuleInput['artifactType'] {
  return type === ArtifactType.CHART ? 'chart' : 'image';
}

function mapRule(rule: ProtoRoute): RouteRuleInput {
  return {
    id: rule.id,
    clientKey: rule.id,
    artifactType: mapArtifactType(rule.artifactType),
    mode: mapMode(rule.mode),
    sourcePrefix: rule.sourcePrefix,
    targetPrefix: rule.targetPrefix,
  };
}

function mapCluster(cluster: ProtoCluster): ClusterSummary {
  return {
    id: cluster.id,
    name: cluster.name,
    customerId: cluster.customerId,
    enabled: cluster.status !== ClusterStatus.DISABLED,
    version: Number(cluster.version),
    routeCount: cluster.routeCount,
    createdAt: cluster.createdAt ? timestampDate(cluster.createdAt).toISOString() : undefined,
    updatedAt: cluster.updatedAt ? timestampDate(cluster.updatedAt).toISOString() : undefined,
  };
}

function routeToRpc(rule: RouteRuleInput) {
  const modeByValue: Record<RouteRuleInput['mode'], ArtifactMode> = {
    direct: ArtifactMode.DIRECT,
    pull_through_cache: ArtifactMode.PULL_THROUGH_CACHE,
    replicated: ArtifactMode.REPLICATED,
  };
  return {
    id: rule.id ?? '',
    artifactType: rule.artifactType === 'chart' ? ArtifactType.CHART : ArtifactType.IMAGE,
    mode: modeByValue[rule.mode],
    sourcePrefix: rule.sourcePrefix.trim(),
    targetPrefix: rule.targetPrefix.trim(),
    provider: rule.provider ?? '',
  };
}

function requireCluster(cluster: ProtoCluster | undefined): ProtoCluster {
  if (!cluster) throw new ConnectError('cluster response is missing', Code.Internal);
  return cluster;
}

export async function listClusters(customerId: string): Promise<ClusterSummary[]> {
  const response = await client.listClusters({ customerId });
  return response.clusters.map(mapCluster);
}

export async function getCluster(clusterId: string): Promise<Cluster> {
  const [clusterResponse, routeResponse] = await Promise.all([
    client.getCluster({ clusterId }),
    client.getClusterRoutes({ clusterId }),
  ]);
  const summary = mapCluster(requireCluster(clusterResponse.cluster));
  const routes = routeResponse.routes.map(mapRule);
  return {
    ...summary,
    routeCount: routes.length,
    imageRules: routes.filter((route) => route.artifactType === 'image'),
    chartRules: routes.filter((route) => route.artifactType === 'chart'),
  };
}

export async function createCluster(customerId: string, name: string): Promise<Cluster> {
  const response = await client.createCluster({ customerId, name });
  return { ...mapCluster(requireCluster(response.cluster)), imageRules: [], chartRules: [] };
}

export async function updateCluster(clusterId: string, input: ClusterFormInput): Promise<Cluster> {
  const response = await client.updateCluster({
    clusterId,
    name: input.name.trim(),
    enabled: input.enabled,
    version: BigInt(input.version),
    routes: [...input.imageRules, ...input.chartRules].map(routeToRpc),
  });
  const summary = mapCluster(requireCluster(response.cluster));
  const routes = response.routes.map(mapRule);
  return {
    ...summary,
    routeCount: routes.length,
    imageRules: routes.filter((route) => route.artifactType === 'image'),
    chartRules: routes.filter((route) => route.artifactType === 'chart'),
  };
}

export async function disableCluster(clusterId: string): Promise<void> {
  await client.disableCluster({ clusterId });
}

const ERROR_CODE_NAMES: Partial<Record<Code, string>> = {
  [Code.PermissionDenied]: 'permission_denied',
  [Code.NotFound]: 'not_found',
  [Code.Aborted]: 'optimistic_lock_conflict',
};

function mapViolationField(field: string, input?: ClusterFormInput): string {
  const match = field.match(/^routes\[(\d+)]\.(.+)$/);
  if (!match || !input) return field;

  const routeIndex = Number(match[1]);
  if (routeIndex < input.imageRules.length) return `imageRules[${routeIndex}].${match[2]}`;
  return `chartRules[${routeIndex - input.imageRules.length}].${match[2]}`;
}

export function mapSaveError(error: unknown, input?: ClusterFormInput): SaveError {
  const connectError = ConnectError.from(error);
  if (connectError.code === Code.Unavailable) {
    return { code: 'network_error', message: 'Unable to connect to the server. Your draft has been preserved.' };
  }

  const detail = connectError.findDetails(RouteValidationDetailSchema)[0];
  const rawMessage = connectError.rawMessage;
  const messageCode = rawMessage.match(/^(routing_conflict|invalid_uri|mode_not_supported|optimistic_lock_conflict|credential_not_allowed|invalid_name):/)?.[1];
  return {
    code: detail?.errorCode || messageCode || ERROR_CODE_NAMES[connectError.code] || 'unknown',
    message: detail?.description || rawMessage || 'Save failed',
    fieldViolations: detail?.field
      ? [{ field: mapViolationField(detail.field, input), description: detail.description || rawMessage }]
      : undefined,
    conflictingRuleId: detail?.conflictingRuleId || undefined,
  };
}
