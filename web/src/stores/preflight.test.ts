import { create } from '@bufbuild/protobuf';
import { Code, ConnectError, type Client } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  GetOperationResponseSchema,
  OperationSchema,
  OperationStatus,
  OrchestratorService,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { setOperationClientForTest } from '@/connect/operation-api';
import { usePreflightStore } from './preflight';

function operationResponse(state: OperationStatus) {
  return create(GetOperationResponseSchema, {
    operation: create(OperationSchema, {
      operationId: 'op-1',
      operationType: 'INSTALL',
      state,
      lastError: '',
    }),
  });
}

describe('preflight store', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    setActivePinia(createPinia());
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('polls while preflight is active and stops at a terminal state', async () => {
    const getOperation = vi.fn()
      .mockResolvedValueOnce(operationResponse(OperationStatus.PREFLIGHT))
      .mockResolvedValueOnce(operationResponse(OperationStatus.FAILED));
    setOperationClientForTest({ getOperation } as unknown as Client<typeof OrchestratorService>);
    const store = usePreflightStore();

    await store.load('op-1');
    expect(store.polling).toBe(true);
    expect(store.operation?.state).toBe('preflight');

    await vi.advanceTimersByTimeAsync(3000);

    expect(getOperation).toHaveBeenCalledTimes(2);
    expect(store.polling).toBe(false);
    expect(store.operation?.state).toBe('failed');
  });

  it('cleans the polling timer when the page unmounts', async () => {
    const getOperation = vi.fn().mockResolvedValue(operationResponse(OperationStatus.PREFLIGHT));
    setOperationClientForTest({ getOperation } as unknown as Client<typeof OrchestratorService>);
    const store = usePreflightStore();

    await store.load('op-1');
    store.stopPolling();
    await vi.advanceTimersByTimeAsync(6000);

    expect(getOperation).toHaveBeenCalledTimes(1);
  });

  it('keeps the last server result when a polling request has a network error', async () => {
    const getOperation = vi.fn()
      .mockResolvedValueOnce(operationResponse(OperationStatus.PREFLIGHT))
      .mockRejectedValueOnce(new ConnectError('network down', Code.Unavailable))
      .mockResolvedValueOnce(operationResponse(OperationStatus.SUCCEEDED));
    setOperationClientForTest({ getOperation } as unknown as Client<typeof OrchestratorService>);
    const store = usePreflightStore();

    await store.load('op-1');
    await vi.advanceTimersByTimeAsync(3000);

    expect(store.operation?.state).toBe('preflight');
    expect(store.error).toContain('网络错误');
    expect(store.polling).toBe(true);

    await vi.advanceTimersByTimeAsync(3000);
    expect(store.operation?.state).toBe('succeeded');
    expect(store.error).toBeNull();
    expect(store.polling).toBe(false);
  });
});
