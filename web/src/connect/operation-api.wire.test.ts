import { create, fromBinary, toBinary, type MessageInitShape } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import {
  ListNonTerminalOperationsRequestSchema,
  ListNonTerminalOperationsResponseSchema,
  NonTerminalOperationSummarySchema,
  type ListNonTerminalOperationsRequest,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { isMaintenanceError, listNonTerminalOperations, mapOperationError } from './operation-api';

/*
 * Wire-shape tests for the cross-release non-terminal read (TASK-276 / TASK-277 AC-01).
 *
 * Every page test injects a client, so nothing else guards the mapping from the typed
 * query to the Connect message, or the response back. These go through the REAL client
 * and a stubbed fetch: the request body is decoded with the same schema the server uses
 * (`fromBinary`), and the response is a real binary protobuf frame — no input is mocked,
 * so an argument the wrapper silently drops cannot pass.
 */
function stubFetch(build: () => Response) {
  const calls: Array<{ url: string; bytes: Uint8Array }> = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = new Request(input as RequestInfo, init);
      calls.push({ url: request.url, bytes: new Uint8Array(await request.arrayBuffer()) });
      return build();
    }),
  );
  return calls;
}

function binaryResponse(
  operations: Array<MessageInitShape<typeof NonTerminalOperationSummarySchema>>,
  nextPageToken = '',
): Response {
  const message = create(ListNonTerminalOperationsResponseSchema, {
    operations: operations.map((row) => create(NonTerminalOperationSummarySchema, row)),
    nextPageToken,
  });
  return new Response(toBinary(ListNonTerminalOperationsResponseSchema, message), {
    status: 200,
    headers: { 'Content-Type': 'application/proto' },
  });
}

function connectErrorResponse(code: string, message: string, status = 503): Response {
  return new Response(JSON.stringify({ code, message }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function sentRequest(calls: Array<{ bytes: Uint8Array }>, index = 0): ListNonTerminalOperationsRequest {
  return fromBinary(ListNonTerminalOperationsRequestSchema, calls[index]!.bytes);
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('ListNonTerminalOperations wire shape', () => {
  it('sends the server defaults when no query is given', async () => {
    const calls = stubFetch(() => binaryResponse([]));

    await listNonTerminalOperations();

    expect(calls[0]!.url).toContain('/orchestrator.v1.OrchestratorService/ListNonTerminalOperations');
    const sent = sentRequest(calls);
    // 0 (not 20) selects the server default; the wrapper must not pre-empt that policy.
    expect(sent.pageSize).toBe(0);
    expect(sent.pageToken).toBe('');
    expect(sent.customerId).toBe('');
  });

  it('passes page_size, page_token and customer_id through unclamped', async () => {
    const calls = stubFetch(() => binaryResponse([]));

    // 250 is above the server's cap of 100 and the wrapper must NOT clamp it: the
    // clamping rule belongs to contracts.NormalizePageSize, not to the console.
    await listNonTerminalOperations({ pageSize: 250, pageToken: 'cursor-2', customerId: 'cust-9' });

    const sent = sentRequest(calls);
    expect(sent.pageSize).toBe(250);
    expect(sent.pageToken).toBe('cursor-2');
    expect(sent.customerId).toBe('cust-9');
  });

  it('decodes the aggregate rows, the inline identities and the next page token', async () => {
    const calls = stubFetch(() =>
      binaryResponse(
        [
          {
            operationId: 'op-1',
            operationType: 'UPGRADE',
            state: 'running',
            releaseDefinitionId: 'def-1',
            releaseDefinitionName: 'checkout',
            customerId: 'cust-1',
            customerName: 'Acme',
            clusterId: 'cluster-1',
            createdAt: timestampFromDate(new Date('2026-10-01T00:00:00Z')),
            updatedAt: timestampFromDate(new Date('2026-10-01T00:05:00Z')),
            revision: 7,
          },
          {
            operationId: 'op-2',
            // The store string, not the timedelta enum: an unknown type must still be
            // readable rather than render as an empty cell.
            operationType: 'EMERGENCY',
            state: 'CANCELLING',
            releaseDefinitionId: 'def-2',
            releaseDefinitionName: '',
            customerId: 'cust-2',
            customerName: '',
            clusterId: 'cluster-2',
            emergency: true,
            revision: 0,
          },
        ],
        'cursor-next',
      ),
    );

    const page = await listNonTerminalOperations({ pageSize: 2 });

    expect(sentRequest(calls).pageSize).toBe(2);
    expect(page.nextPageToken).toBe('cursor-next');
    expect(page.operations).toEqual([
      {
        operationId: 'op-1',
        operationType: 'UPGRADE',
        state: 'running',
        releaseDefinitionId: 'def-1',
        releaseDefinitionName: 'checkout',
        customerId: 'cust-1',
        customerName: 'Acme',
        clusterId: 'cluster-1',
        createdAt: '2026-10-01T00:00:00.000Z',
        updatedAt: '2026-10-01T00:05:00.000Z',
        emergency: false,
        revision: 7,
      },
      {
        operationId: 'op-2',
        operationType: 'EMERGENCY',
        // Lower-cased through the same normaliser the per-release history uses.
        state: 'cancelling',
        releaseDefinitionId: 'def-2',
        releaseDefinitionName: '',
        customerId: 'cust-2',
        customerName: '',
        clusterId: 'cluster-2',
        createdAt: null,
        updatedAt: null,
        emergency: true,
        revision: 0,
      },
    ]);
  });

  it('recognises the maintenance refusal exactly', async () => {
    stubFetch(() => connectErrorResponse('unavailable', 'maintenance'));

    const error = await listNonTerminalOperations({ pageToken: 'cursor-2' }).catch((thrown: unknown) => thrown);

    expect(isMaintenanceError(error)).toBe(true);
    // The shared mapper is deliberately UNCHANGED: the operation stream reads
    // `network_error` as "reconnect", and maintenance must not silently rewrite that
    // for the write paths that also route through it.
    expect(mapOperationError(error).code).toBe('network_error');
  });

  it('does not mistake a transport outage for maintenance', async () => {
    stubFetch(() => connectErrorResponse('unavailable', 'connection refused'));

    const error = await listNonTerminalOperations().catch((thrown: unknown) => thrown);

    expect(isMaintenanceError(error)).toBe(false);
    expect(mapOperationError(error).code).toBe('network_error');
  });

  it('maps an out-of-scope refusal to permission_denied', async () => {
    stubFetch(() => connectErrorResponse('permission_denied', 'customer not bound to caller', 403));

    const error = await listNonTerminalOperations({ customerId: 'cust-theirs' }).catch((thrown: unknown) => thrown);

    expect(isMaintenanceError(error)).toBe(false);
    expect(mapOperationError(error).code).toBe('permission_denied');
    expect(mapOperationError(error).retryable).toBe(false);
  });
});
