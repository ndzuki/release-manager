import { ConnectError } from '@connectrpc/connect';
import { fromBinary } from '@bufbuild/protobuf';
import {
  ExecuteEmergencyChangeRequestSchema,
  type ExecuteEmergencyChangeRequest,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { executeEmergencyChange, type ExecuteEmergencyInput } from './emergency-api';

/*
 * Wire-shape tests for the emergency execution seam (TASK-273).
 *
 * Every store/UI test injects the executor, so the mapping from the input
 * object to the Connect message had no guard at all: the old mapper accepted a
 * container + artifactRef and silently dropped the replicas / annotations
 * payloads, which is exactly the "read side wants X, write side produces Y"
 * drift this file now pins. These go through the REAL client and a stubbed
 * fetch, then decode the binary body with the same schema the server uses, so
 * the assertion is on the bytes the backend would receive — not on a mock.
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

function sentRequest(calls: Array<{ bytes: Uint8Array }>): ExecuteEmergencyChangeRequest {
  expect(calls).toHaveLength(1);
  return fromBinary(ExecuteEmergencyChangeRequestSchema, calls[0]!.bytes);
}

/** Which of the three mutually exclusive actions the request actually carries. */
function populatedActions(request: ExecuteEmergencyChangeRequest): string[] {
  const actions: string[] = [];
  if (request.container !== '' || request.artifactRef !== '') actions.push('image');
  if (request.setReplicas !== 0) actions.push('replicas');
  if (request.annotations.length > 0 || request.annotationScope !== '') actions.push('annotations');
  return actions;
}

const BASE = {
  releaseDefinitionId: 'def-1',
  workloadRef: 'deployments/ns1/api',
  operationVersion: '',
  convergenceStrategy: 'REVERT_ON_NEXT_RECONCILE' as const,
  targetLocks: [],
  idempotencyKey: 'key-1',
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('executeEmergencyChange wire shape', () => {
  it('image-only sends container + artifact_ref and zeroes the other actions', async () => {
    const calls = stubFetch({ code: 'internal', message: 'boom' });
    const input: ExecuteEmergencyInput = { ...BASE, action: 'image', container: 'app', artifactRef: 'artifact-9' };

    await expect(executeEmergencyChange(input)).rejects.toBeInstanceOf(ConnectError);

    expect(calls[0]!.url).toContain('/orchestrator.v1.OrchestratorService/ExecuteEmergencyChange');
    const sent = sentRequest(calls);
    expect(sent.container).toBe('app');
    expect(sent.artifactRef).toBe('artifact-9');
    expect(sent.setReplicas).toBe(0);
    expect(sent.annotations).toEqual([]);
    expect(sent.annotationScope).toBe('');
    expect(populatedActions(sent)).toEqual(['image']);
    expect(sent.idempotencyKey).toBe('key-1');
    expect(sent.workloadRef).toBe('deployments/ns1/api');
  });

  it('replicas-only sends the flat set_replicas scalar and nothing else', async () => {
    const calls = stubFetch({ code: 'internal', message: 'boom' });
    const input: ExecuteEmergencyInput = { ...BASE, action: 'replicas', setReplicas: 3 };

    await expect(executeEmergencyChange(input)).rejects.toBeInstanceOf(ConnectError);

    const sent = sentRequest(calls);
    expect(sent.setReplicas).toBe(3);
    expect(sent.container).toBe('');
    expect(sent.artifactRef).toBe('');
    expect(sent.annotations).toEqual([]);
    expect(sent.annotationScope).toBe('');
    expect(populatedActions(sent)).toEqual(['replicas']);
  });

  it('annotations-only sends every entry with its scope and no image/scalar fields', async () => {
    const calls = stubFetch({ code: 'internal', message: 'boom' });
    const input: ExecuteEmergencyInput = {
      ...BASE,
      action: 'annotations',
      annotations: [
        { key: 'tier', value: 'web' },
        { key: 'prometheus.io/scrape', value: 'true' },
      ],
      annotationScope: 'WORKLOAD_METADATA',
    };

    await expect(executeEmergencyChange(input)).rejects.toBeInstanceOf(ConnectError);

    const sent = sentRequest(calls);
    expect(sent.annotations.map((entry) => ({ key: entry.key, value: entry.value }))).toEqual([
      { key: 'tier', value: 'web' },
      { key: 'prometheus.io/scrape', value: 'true' },
    ]);
    expect(sent.annotationScope).toBe('WORKLOAD_METADATA');
    expect(sent.container).toBe('');
    expect(sent.artifactRef).toBe('');
    expect(sent.setReplicas).toBe(0);
    expect(populatedActions(sent)).toEqual(['annotations']);
  });

  // The "combination" case: the server refuses two actions in one request
  // (conflicting_change, internal/orchestrator/emergency.go:1065-1083), so the
  // mapper must never let state from another action leak onto the wire. Each of
  // the three inputs above carries exactly ONE populated action, and the
  // replicas/annotations ones must not inherit the image fields.
  it('never serializes two actions into one request', async () => {
    const inputs: ExecuteEmergencyInput[] = [
      { ...BASE, action: 'image', container: 'app', artifactRef: 'artifact-9' },
      { ...BASE, action: 'replicas', setReplicas: 2 },
      { ...BASE, action: 'annotations', annotations: [{ key: 'tier', value: 'web' }], annotationScope: 'WORKLOAD_METADATA' },
    ];

    for (const input of inputs) {
      const calls = stubFetch({ code: 'internal', message: 'boom' });
      await expect(executeEmergencyChange(input)).rejects.toBeInstanceOf(ConnectError);
      expect(populatedActions(sentRequest(calls)), `input ${input.action}`).toEqual([input.action]);
      vi.unstubAllGlobals();
    }
  });
});
