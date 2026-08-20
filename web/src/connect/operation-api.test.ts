import { create } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { Code, ConnectError, type Client } from '@connectrpc/connect';
import { describe, expect, it, vi } from 'vitest';
import {
  BundleService,
  BundleSummarySchema,
  CreateOperationResponseSchema,
  GetOperationResponseSchema,
  ListBundlesResponseSchema,
  OperationSchema,
  OperationStatus,
  OrchestratorService,
  CancelOperationResponseSchema,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { BundleStatus } from '@/gen/common/v1/domain_pb';
import {
  cancelOperation,
  createOperation,
  getOperation,
  loadOperationOptions,
  mapOperationError,
  setOperationClientForTest,
  watchOperation,
} from './operation-api';

function bundleClientMock(): Client<typeof BundleService> {
  return {
    listBundles: vi.fn().mockResolvedValue(create(ListBundlesResponseSchema, {
      bundles: [create(BundleSummarySchema, {
        id: 'bundle-1',
        name: 'app',
        digest: { algorithm: 'sha256', value: 'sha256:bundle' },
        status: BundleStatus.VALIDATED,
        chartRef: 'oci://registry/app',
        chartVersion: '1.0.0',
        chartDigest: 'sha256:chart',
        images: [{ ref: 'registry/app:v1', digest: 'sha256:image', valuesPath: 'image' }],
        createdAt: timestampFromDate(new Date('2026-07-22T00:00:00Z')),
      })],
    })),
  } as unknown as Client<typeof BundleService>;
}

function operationClientMock(overrides: Partial<Client<typeof OrchestratorService>> = {}): Client<typeof OrchestratorService> {
  return {
    getOperation: vi.fn().mockResolvedValue(create(GetOperationResponseSchema, {
      operation: create(OperationSchema, {
        operationId: 'op-1',
        operationType: 'INSTALL',
        state: OperationStatus.FAILED,
        lastError: 'boom',
      }),
    })),
    createOperation: vi.fn().mockResolvedValue(create(CreateOperationResponseSchema, {
      operationId: 'op-1',
      state: 'preflight',
      preflightId: 'pf-1',
    })),
    ...overrides,
  } as unknown as Client<typeof OrchestratorService>;
}

describe('operation API', () => {
  it('loads only validated bundles as form options', async () => {
    const testClient = operationClientMock();
    const testBundleClient = bundleClientMock();
    setOperationClientForTest(testClient, testBundleClient);

    const options = await loadOperationOptions('def-1');

    expect(options.bundles).toHaveLength(1);
    expect(options.bundles[0]).toEqual(expect.objectContaining({
      bundleId: 'bundle-1',
      name: 'app',
      status: 'validated',
      chartVersion: '1.0.0',
    }));
    expect(testBundleClient.listBundles).toHaveBeenCalledWith(expect.objectContaining({
      releaseDefinitionId: 'def-1',
      statusFilter: [BundleStatus.VALIDATED],
    }));
  });

  it('submits values as a JSON merge patch without actor or credential fields', async () => {
    const testClient = operationClientMock();
    setOperationClientForTest(testClient);

    await createOperation({
      idempotencyKey: 'idem-1',
      releaseDefinitionId: 'def-1',
      operationType: 'UPGRADE',
      bundleId: 'bundle-1',
      expectedCurrentRevision: 7,
      valuesRevisionId: 'vr-1',
      patch: [{ path: 'image.tag', value: 'v2', kind: 'LITERAL' }],
    });

    const payload = vi.mocked(testClient.createOperation).mock.calls[0]?.[0];
    const options = vi.mocked(testClient.createOperation).mock.calls[0]?.[1];
    expect(payload).toEqual(expect.objectContaining({
      operationType: 'UPGRADE',
      bundleId: 'bundle-1',
      releaseDefinitionId: 'def-1',
      expectedCurrentRevision: 7,
      valuesRevisionId: 'vr-1',
      valuesPatch: { 'image.tag': 'v2' },
    }));
    expect(new Headers(options?.headers).get('Idempotency-Key')).toBe('idem-1');
    expect(payload).not.toHaveProperty('actor');
    expect(JSON.stringify(payload).toLowerCase()).not.toMatch(/password|access_token|refresh_token/);
  });

  it('maps the rich Operation message from GetOperation', async () => {
    const testClient = operationClientMock();
    testClient.getOperation = vi.fn().mockResolvedValue(create(GetOperationResponseSchema, {
      operation: create(OperationSchema, {
        operationId: 'op-1',
        releaseDefinitionId: 'def-1',
        operationType: 'UPGRADE',
        state: OperationStatus.RUNNING,
        stateVersion: 3n,
        bundleId: 'bundle-1',
        valuesRevisionId: 'vr-1',
        expectedRevision: 4,
        targetRevision: 5,
        lastError: '',
      }),
    }));
    setOperationClientForTest(testClient);

    const operation = await getOperation('op-1');

    expect(operation).toEqual(expect.objectContaining({
      operationId: 'op-1',
      releaseDefinitionId: 'def-1',
      operationType: 'UPGRADE',
      state: 'running',
      stateVersion: 3n,
      bundleId: 'bundle-1',
      valuesRevisionId: 'vr-1',
      expectedRevision: 4,
      targetRevision: 5,
      lastError: '',
    }));
  });


  it('maps release_busy to the existing operation link', () => {
    const error = new ConnectError('release busy', Code.FailedPrecondition, {
      'X-Reason-Code': 'release_busy',
      'X-Operation-ID': 'op-active',
    });

    expect(mapOperationError(error)).toEqual({
      code: 'release_busy',
      message: 'Release 有进行中的操作',
      operationId: 'op-active',
      retryable: false,
    });
  });

  it('watchOperation opens the stream with after_sequence and forwards the abort signal', async () => {
    const watch = vi.fn().mockResolvedValue((async function* () {})());
    const testClient = operationClientMock({ watchOperation: watch });
    setOperationClientForTest(testClient);

    const controller = new AbortController();
    const stream = await watchOperation('op-1', 42n, controller.signal);

    expect(stream).toBeDefined();
    expect(watch).toHaveBeenCalledTimes(1);
    const [request, options] = watch.mock.calls[0] as [never, { signal: AbortSignal }];
    expect(request).toEqual(expect.objectContaining({ operationId: 'op-1', afterSequence: 42n }));
    expect(options.signal).toBe(controller.signal);
  });

  it('AC-08: cancelOperation sends reason/expected_state_version with an Idempotency-Key header', async () => {
    const cancel = vi.fn().mockResolvedValue(create(CancelOperationResponseSchema, {
      operation: create(OperationSchema, {
        operationId: 'op-1',
        operationType: 'INSTALL',
        state: OperationStatus.CANCELLING,
        stateVersion: 2n,
      }),
      requestId: 'req-9',
    }));
    const testClient = operationClientMock({ cancelOperation: cancel });
    setOperationClientForTest(testClient);

    const result = await cancelOperation({
      operationId: 'op-1',
      reason: '业务原因',
      expectedStateVersion: 1n,
      idempotencyKey: 'key-1',
    });

    expect(result.requestId).toBe('req-9');
    expect(result.operation.state).toBe('cancelling');
    expect(result.operation.stateVersion).toBe(2n);
    const [request, options] = cancel.mock.calls[0] as [never, { headers: HeadersInit }];
    expect(request).toEqual(expect.objectContaining({
      operationId: 'op-1',
      reason: '业务原因',
      expectedStateVersion: 1n,
    }));
    expect(new Headers(options?.headers).get('Idempotency-Key')).toBe('key-1');
  });

  it('cursor_expired decodes snapshot sequence headers and the proto header', () => {
    const error = new ConnectError('cursor_expired: retained timeline no longer includes the requested sequence', Code.OutOfRange, {
      'X-Reason-Code': 'cursor_expired',
      'X-Snapshot-Sequence': '42',
      'X-Retained-From-Sequence': '10',
      'X-Snapshot-Proto': 'eyJvcGVyYXRpb24iOnsiaWQiOiIxIn19',
    });

    const mapped = mapOperationError(error);

    expect(mapped.code).toBe('cursor_expired');
    expect(mapped.retryable).toBe(true);
    expect(mapped.snapshotSequence).toBe(42n);
    expect(mapped.retainedFromSequence).toBe(10n);
    expect(mapped.snapshotProto).toBe('eyJvcGVyYXRpb24iOnsiaWQiOiIxIn19');
  });
});
