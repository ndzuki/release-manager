import { create } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import type { Client } from '@connectrpc/connect';
import { describe, expect, it, vi } from 'vitest';
import { ClusterSchema, ClusterStatus } from '@/gen/common/v1/domain_pb';
import {
  OrchestratorService,
  RouteValidationDetailSchema,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import type { ClusterFormInput } from '@/types/cluster';
import { mapSaveError, setClusterClientForTest, updateCluster } from './cluster-api';

const input: ClusterFormInput = {
  name: 'staging',
  enabled: true,
  version: 1,
  imageRules: [],
  chartRules: [],
};

function mockClient(overrides: Partial<Client<typeof OrchestratorService>> = {}) {
  return {
    updateCluster: vi.fn().mockResolvedValue({
      cluster: create(ClusterSchema, {
        id: 'cluster-1',
        name: 'staging',
        customerId: 'customer-1',
        status: ClusterStatus.ACTIVE,
        version: 2n,
        routeCount: 0,
      }),
      routes: [],
    }),
    ...overrides,
  } as unknown as Client<typeof OrchestratorService>;
}

describe('updateCluster', () => {
  it('submits only the typed cluster contract without credential fields', async () => {
    const testClient = mockClient();
    setClusterClientForTest(testClient);

    await updateCluster('cluster-1', input);

    const payload = vi.mocked(testClient.updateCluster).mock.calls[0]?.[0];
    expect(payload).toEqual({
      clusterId: 'cluster-1',
      name: 'staging',
      enabled: true,
      version: 1n,
      routes: [],
    });
    expect(JSON.stringify(payload, (_key, value) => typeof value === 'bigint' ? value.toString() : value).toLowerCase())
      .not.toMatch(/credential|password|token|secret/);
  });
});

describe('mapSaveError', () => {
  it('maps a network failure to a retryable draft-preserving error', () => {
    expect(mapSaveError(ConnectError.from(new TypeError('Failed to fetch'), Code.Unavailable))).toEqual({
      code: 'network_error',
      message: 'Unable to connect to the server. Your draft has been preserved.',
    });
  });

  it('decodes structured route validation details', () => {
    const error = new ConnectError('route source prefix conflicts with another rule', Code.AlreadyExists, undefined, [
      {
        desc: RouteValidationDetailSchema,
        value: {
          errorCode: 'routing_conflict',
          field: 'routes[1].sourcePrefix',
          description: 'route source prefix conflicts with another rule',
          conflictingRuleId: 'rule-1',
        },
      },
    ]);

    expect(mapSaveError(error, {
      ...input,
      imageRules: [{
        clientKey: 'image-rule',
        artifactType: 'image',
        mode: 'direct',
        sourcePrefix: 'docker.io/library/',
        targetPrefix: 'harbor.example.com/proxy/',
      }],
      chartRules: [{
        clientKey: 'chart-rule',
        artifactType: 'chart',
        mode: 'direct',
        sourcePrefix: 'charts.example.com/',
        targetPrefix: 'harbor.example.com/charts/',
      }],
    })).toEqual({
      code: 'routing_conflict',
      message: 'route source prefix conflicts with another rule',
      fieldViolations: [{
        field: 'chartRules[0].sourcePrefix',
        description: 'route source prefix conflicts with another rule',
      }],
      conflictingRuleId: 'rule-1',
    });
  });

  it.each([
    ['routing_conflict', 'routing_conflict: route source prefix conflicts with another rule'],
    ['optimistic_lock_conflict', 'optimistic_lock_conflict: data was modified by another user'],
    ['credential_not_allowed', 'credential_not_allowed: registry credentials are not accepted'],
  ])('extracts %s from the server message', (code, message) => {
    expect(mapSaveError(new ConnectError(message)).code).toBe(code);
  });
});
