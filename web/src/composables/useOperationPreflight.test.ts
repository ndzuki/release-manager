import { flushPromises } from '@vue/test-utils';
import { Code, ConnectError } from '@connectrpc/connect';
import { effectScope, nextTick, ref, type EffectScope } from 'vue';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { t } from '@/i18n/messages';
import type { PreflightResult } from '@/types/operation';
import { useOperationPreflight } from './useOperationPreflight';

/*
 * The preflight read is part of the operation detail page but independent of the
 * stream: it is fetched alongside it, re-read when the operation moves on, and it
 * must stay distinguishable from "no preflight result yet" when the read fails
 * (TASK-281). These cases pin that contract at the composable seam the page now
 * drives.
 */

function preflightResult(overall: PreflightResult['overall'] = 'passed'): PreflightResult {
  return { overall, failedStage: '', stages: [], errorCode: '' };
}

interface Harness {
  api: ReturnType<typeof useOperationPreflight>;
  operationId: ReturnType<typeof ref<string>>;
  scopeKey: ReturnType<typeof ref<string>>;
  stateVersion: ReturnType<typeof ref<bigint | undefined>>;
  fetchResult: ReturnType<typeof vi.fn>;
  scope: EffectScope;
}

function setup(
  fetchResult: (operationId: string) => Promise<PreflightResult | null> = vi.fn(async () => preflightResult()),
): Harness {
  const operationId = ref('op-1');
  const scopeKey = ref('cust-1/cluster-1/def-1/op-1');
  const stateVersion = ref<bigint | undefined>(1n);
  const fetch = vi.fn(fetchResult);
  const scope = effectScope();
  const api = scope.run(() =>
    useOperationPreflight({
      operationId: () => operationId.value,
      scopeKey: () => scopeKey.value,
      stateVersion: () => stateVersion.value,
      fetchResult: fetch,
    }),
  )!;
  return { api, operationId, scopeKey, stateVersion, fetchResult: fetch, scope };
}

let harness: Harness | null = null;
afterEach(() => {
  harness?.scope.stop();
  harness = null;
});

describe('useOperationPreflight', () => {
  it('loads the preflight result for the operation on screen', async () => {
    harness = setup();
    await flushPromises();

    expect(harness.fetchResult).toHaveBeenCalledWith('op-1');
    expect(harness.api.result.value).toEqual(preflightResult());
    expect(harness.api.failure.value).toBeNull();
  });

  it('keeps a missing result distinguishable from a failed read', async () => {
    harness = setup(async () => null);
    await flushPromises();

    // No result yet is not a failure: the panel stays absent, the banner stays off.
    expect(harness.api.result.value).toBeNull();
    expect(harness.api.failure.value).toBeNull();
  });

  it('surfaces a failed read with the retry copy and the correlation line', async () => {
    harness = setup(async () => {
      throw new ConnectError('preflight read failed', Code.Internal, { 'X-Request-ID': 'req-preflight-1' });
    });
    await flushPromises();

    expect(harness.api.result.value).toBeNull();
    expect(harness.api.failure.value?.message).toBe(t('operation.preflight.loadFailedHint'));
    expect(harness.api.failure.value?.details).toContain('requestId=req-preflight-1');
  });

  it('drops a late result for an operation the page has left', async () => {
    let resolveFirst!: (value: PreflightResult) => void;
    harness = setup(() => new Promise((resolve) => {
      // Keep the FIRST read's resolver: the second read (for the new operation)
      // must stay in flight so only the stale one can settle.
      resolveFirst ??= resolve;
    }));
    // The composable calls the read once on creation (scope watch, immediate).
    expect(harness.fetchResult).toHaveBeenCalledTimes(1);

    harness.operationId.value = 'op-2';
    await nextTick();

    // The first read now resolves for the operation that is no longer on screen.
    resolveFirst(preflightResult());
    await flushPromises();

    expect(harness.api.result.value).toBeNull();
    expect(harness.api.failure.value).toBeNull();
  });

  it('clears the previous read as soon as the scope changes', async () => {
    let reads = 0;
    harness = setup(async () => {
      reads += 1;
      // The first scope resolves; every later read stays in flight.
      return reads === 1 ? preflightResult() : new Promise<PreflightResult | null>(() => undefined);
    });
    await flushPromises();
    expect(harness.api.result.value).not.toBeNull();

    harness.scopeKey.value = 'cust-1/cluster-2/def-2/op-2';
    await nextTick();

    // The new read is still in flight (the mock resolves on the next flush), so
    // what is asserted is the reset, not the replacement.
    expect(harness.api.result.value).toBeNull();
    expect(harness.api.failure.value).toBeNull();
  });

  it('re-reads when the operation state version advances', async () => {
    harness = setup();
    await flushPromises();
    expect(harness.fetchResult).toHaveBeenCalledTimes(1);

    harness.stateVersion.value = 2n;
    await nextTick();
    await flushPromises();

    expect(harness.fetchResult).toHaveBeenCalledTimes(2);
    expect(harness.fetchResult).toHaveBeenLastCalledWith('op-1');
  });

  it('does not read when the route carries no operation id', async () => {
    harness = setup();
    harness.operationId.value = '';
    harness.scopeKey.value = '';
    await nextTick();
    await flushPromises();

    expect(harness.api.result.value).toBeNull();
    expect(harness.api.failure.value).toBeNull();
  });
});
