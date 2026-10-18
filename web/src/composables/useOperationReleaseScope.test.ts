import { flushPromises } from '@vue/test-utils';
import { effectScope, nextTick, ref, type EffectScope } from 'vue';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { DefinitionView } from '@/connect/definition-api';
import {
  useOperationReleaseScope,
  type ReleaseScope,
} from './useOperationReleaseScope';

/*
 * TASK-279: the scope-less Operation-centre route recovers the release context from
 * the operation's own release definition. The read is asynchronous while the
 * operation stream is already live, so every late response has to be checked against
 * the operation (and route) that is on screen *now* (TASK-286 adds the same-id scope
 * switch). These cases pin those guards at the composable seam.
 */

interface Harness {
  api: ReturnType<typeof useOperationReleaseScope>;
  operationId: ReturnType<typeof ref<string>>;
  routeScope: ReturnType<typeof ref<ReleaseScope | null>>;
  routeScopeKey: ReturnType<typeof ref<string>>;
  scopedRoutesAvailable: ReturnType<typeof ref<boolean>>;
  storeOperationId: ReturnType<typeof ref<string | null>>;
  storeDefinitionId: ReturnType<typeof ref<string>>;
  fetchDefinition: ReturnType<typeof vi.fn>;
  scope: EffectScope;
}

function definition(customerId = 'cust-1', clusterId = 'cluster-1'): DefinitionView {
  return { customerId, clusterId } as unknown as DefinitionView;
}

function setup(overrides: {
  routeScope?: ReleaseScope | null;
  storeOperationId?: string | null;
  scopedRoutesAvailable?: boolean;
  fetchDefinition?: (definitionId: string) => Promise<DefinitionView>;
} = {}): Harness {
  const routeScope = ref<ReleaseScope | null>(overrides.routeScope ?? null);
  const routeScopeKey = ref(overrides.routeScope ? 'cust-1/cluster-1/def-1/op-1' : '///op-1');
  const operationId = ref('op-1');
  const scopedRoutesAvailable = ref(overrides.scopedRoutesAvailable ?? true);
  const storeOperationId = ref<string | null>(overrides.storeOperationId ?? 'op-1');
  const storeDefinitionId = ref('def-1');
  const fetchDefinition = vi.fn(overrides.fetchDefinition ?? (async () => definition()));
  const scope = effectScope();
  const api = scope.run(() =>
    useOperationReleaseScope({
      operationId: () => operationId.value,
      routeScope: () => routeScope.value,
      routeScopeKey: () => routeScopeKey.value,
      scopedRoutesAvailable: () => scopedRoutesAvailable.value,
      storeOperationId: () => storeOperationId.value,
      storeDefinitionId: () => storeDefinitionId.value,
      fetchDefinition,
    }),
  )!;
  return {
    api,
    operationId,
    routeScope,
    routeScopeKey,
    scopedRoutesAvailable,
    storeOperationId,
    storeDefinitionId,
    fetchDefinition,
    scope,
  };
}

let harness: Harness | null = null;
afterEach(() => {
  harness?.scope.stop();
  harness = null;
});

describe('useOperationReleaseScope', () => {
  it('takes the scope from the route when the URL carries one', async () => {
    harness = setup({
      routeScope: { customerId: 'cust-1', clusterId: 'cluster-1', releaseId: 'def-1' },
    });
    await flushPromises();

    expect(harness.api.releaseScope.value).toEqual({
      customerId: 'cust-1',
      clusterId: 'cluster-1',
      releaseId: 'def-1',
    });
    // No definition read is needed for the canonical nested route.
    expect(harness.fetchDefinition).not.toHaveBeenCalled();
  });

  it('recovers the scope from the release definition on the scope-less route', async () => {
    harness = setup();
    await flushPromises();

    expect(harness.fetchDefinition).toHaveBeenCalledWith('def-1');
    expect(harness.api.releaseScope.value).toEqual({
      customerId: 'cust-1',
      clusterId: 'cluster-1',
      releaseId: 'def-1',
    });
  });

  it('leaves the page unscoped when the definition read is refused', async () => {
    harness = setup({ fetchDefinition: async () => Promise.reject(new Error('release:read denied')) });
    await flushPromises();

    expect(harness.api.releaseScope.value).toBeNull();
  });

  it('does not resolve a scope where the release-scoped routes are not registered', async () => {
    harness = setup({ scopedRoutesAvailable: false });
    await flushPromises();

    expect(harness.fetchDefinition).not.toHaveBeenCalled();
    expect(harness.api.releaseScope.value).toBeNull();
  });

  it('drops a definition read that resolves after the route gained its own scope', async () => {
    let resolveStale!: (value: DefinitionView) => void;
    harness = setup({
      fetchDefinition: () => new Promise<DefinitionView>((resolve) => {
        resolveStale = resolve;
      }),
    });
    expect(harness.fetchDefinition).toHaveBeenCalledWith('def-1');

    // The route now carries the scope itself (e.g. the operator navigated to the
    // canonical detail); the in-flight definition read belongs to the old URL.
    harness.routeScope.value = { customerId: 'cust-2', clusterId: 'cluster-2', releaseId: 'def-2' };
    harness.routeScopeKey.value = 'cust-2/cluster-2/def-2/op-1';
    await nextTick();
    resolveStale(definition('cust-9', 'cluster-9'));
    await flushPromises();

    expect(harness.api.releaseScope.value).toEqual({
      customerId: 'cust-2',
      clusterId: 'cluster-2',
      releaseId: 'def-2',
    });
  });

  it('drops a definition read for an operation the store has left', async () => {
    let resolveStale!: (value: DefinitionView) => void;
    harness = setup({
      fetchDefinition: () => new Promise<DefinitionView>((resolve) => {
        resolveStale = resolve;
      }),
    });

    // Same route param, but the store has moved to another operation: only the
    // store's identity can tell that the read no longer applies.
    harness.storeOperationId.value = null;
    await nextTick();
    resolveStale(definition('cust-9', 'cluster-9'));
    await flushPromises();

    expect(harness.api.releaseScope.value).toBeNull();
  });
});
