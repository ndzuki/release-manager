import { fromBinary } from '@bufbuild/protobuf';
import { ConnectError } from '@connectrpc/connect';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  CreateReleaseDefinitionRequestSchema,
  DisableReleaseDefinitionRequestSchema,
  type CreateReleaseDefinitionRequest,
  type DisableReleaseDefinitionRequest,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { createDefinition, disableDefinition } from './definition-api';

/*
 * Wire-shape tests for the two definition writes that had no wrapper at all
 * (TASK-301). The page and store tests inject this module, and definition-api.test.ts
 * spies on the client method, so neither pinned the procedure path or the binary body
 * the transport sends. Decoding with the same schema the Go handler uses keeps the
 * field numbers honest (specifically `enabled`, whose `false` is conveyed by ABSENCE).
 */
function stubFetch(body: unknown, status = 500) {
  const calls: Array<{ url: string; bytes: Uint8Array }> = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = new Request(input as RequestInfo, init);
      calls.push({ url: request.url, bytes: new Uint8Array(await request.arrayBuffer()) });
      return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    }),
  );
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('createDefinition wire shape', () => {
  it('posts the whole create contract to CreateReleaseDefinition', async () => {
    const calls = stubFetch({ code: 'invalid_argument', message: 'customer_id is required' }, 400);

    await expect(
      createDefinition({
        customerId: 'cust-1',
        clusterId: 'cluster-1',
        namespace: 'ns',
        releaseName: 'release-a',
        chartName: 'chart',
        enabled: true,
        promotionMappings: [
          { workloadKind: 'Deployment', workloadName: 'api', container: 'app', field: 'image', valuesPath: 'image.tag' },
        ],
      }),
    ).rejects.toBeInstanceOf(ConnectError);

    expect(calls).toHaveLength(1);
    expect(calls[0]!.url).toContain('/orchestrator.v1.OrchestratorService/CreateReleaseDefinition');
    const sent: CreateReleaseDefinitionRequest = fromBinary(CreateReleaseDefinitionRequestSchema, calls[0]!.bytes);
    expect(sent.customerId).toBe('cust-1');
    expect(sent.clusterId).toBe('cluster-1');
    expect(sent.namespace).toBe('ns');
    expect(sent.releaseName).toBe('release-a');
    expect(sent.chartName).toBe('chart');
    expect(sent.enabled).toBe(true);
    expect(sent.promotionMappings).toHaveLength(1);
    expect(sent.promotionMappings[0]?.valuesPath).toBe('image.tag');
  });

  it('omits `enabled` for a draft, which is how the server reads false', async () => {
    const calls = stubFetch({ code: 'internal', message: 'boom' });

    await expect(
      createDefinition({ customerId: 'c', clusterId: 'k', releaseName: 'r', enabled: false }),
    ).rejects.toBeInstanceOf(ConnectError);

    const sent: CreateReleaseDefinitionRequest = fromBinary(CreateReleaseDefinitionRequestSchema, calls[0]!.bytes);
    expect(sent.enabled).toBe(false);
    expect(sent.customerId).toBe('c');
  });
});

describe('disableDefinition wire shape', () => {
  it('carries the definition id and the version DisableReleaseDefinition compares against', async () => {
    const calls = stubFetch({ code: 'failed_precondition', message: 'optimistic_lock_conflict: changed' }, 400);

    await expect(disableDefinition('def-1', 3n)).rejects.toBeInstanceOf(ConnectError);

    expect(calls).toHaveLength(1);
    expect(calls[0]!.url).toContain('/orchestrator.v1.OrchestratorService/DisableReleaseDefinition');
    const sent: DisableReleaseDefinitionRequest = fromBinary(DisableReleaseDefinitionRequestSchema, calls[0]!.bytes);
    expect(sent.definitionId).toBe('def-1');
    expect(sent.expectedVersion).toBe(3n);
  });
});
