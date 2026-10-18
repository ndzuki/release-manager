import { computed, ref, watch, type ComputedRef } from 'vue';
import { getDefinition, type DefinitionView } from '@/connect/definition-api';

/*
 * Release context resolution for the operation detail page (TASK-279).
 *
 * The canonical nested route carries customer/cluster/release in the URL; the
 * Operation centre's scope-less route (`/operations/:operationId`, TASK-277) carries
 * only the operation id, and vue-router refuses a named route whose required params
 * are empty. Instead of masking every release-scoped action there, the page recovers
 * the context from the operation's own release definition — the same release:read the
 * operation stream already needs. The operation itself renders while that read is in
 * flight or refused; only the actions that need the context wait for it.
 */

export interface ReleaseScope {
  customerId: string;
  clusterId: string;
  releaseId: string;
}

export interface OperationReleaseScopeOptions {
  operationId: () => string;
  /** Scope carried by the route params, or null on the scope-less entry. */
  routeScope: () => ReleaseScope | null;
  /** Stable key of the current route scope, for watcher identity. */
  routeScopeKey: () => string;
  /**
   * False where the release-scoped routes are not registered (release inventory
   * disabled): a resolved definition must not flip the page into scoped rendering.
   */
  scopedRoutesAvailable: () => boolean;
  /** The store's own identity for the operation on screen. */
  storeOperationId: () => string | null;
  /** The release definition the operation on screen addresses. */
  storeDefinitionId: () => string;
  /** Test seam: replaces the release definition read. */
  fetchDefinition?: (definitionId: string) => Promise<DefinitionView>;
}

export interface OperationReleaseScope {
  releaseScope: ComputedRef<ReleaseScope | null>;
}

export function useOperationReleaseScope(
  options: OperationReleaseScopeOptions,
): OperationReleaseScope {
  const fetchDefinition = options.fetchDefinition ?? getDefinition;
  const resolvedScope = ref<ReleaseScope | null>(null);
  const releaseScope = computed(() => options.routeScope() ?? resolvedScope.value);

  /**
   * Recovers the release context from the operation's release definition when the URL
   * does not carry it. Failures are deliberately silent here: the page's own error
   * surface belongs to the operation stream, and a refused definition read must leave
   * the operation readable rather than replace it with an error state.
   */
  async function resolve(definitionId: string): Promise<void> {
    if (!options.scopedRoutesAvailable()) return;
    const requestedOperationId = options.operationId();
    /*
     * The route param alone is not "the operation on screen": across a same-id
     * scope switch it stays the same while the store is reset (operationId null)
     * and reloaded. The store's own identity is the authority for which operation
     * this definition read belongs to; `routeScope()` still stops a route that has
     * since gained its own scope from being overwritten. TASK-286.
     */
    const stillCurrent = (): boolean =>
      options.operationId() === requestedOperationId &&
      !options.routeScope() &&
      options.storeOperationId() === requestedOperationId &&
      options.storeDefinitionId() === definitionId;
    try {
      const definition = await fetchDefinition(definitionId);
      // A late response for a previous operation must not overwrite the context
      // on screen.
      if (!stillCurrent()) return;
      resolvedScope.value =
        definition.customerId && definition.clusterId
          ? { customerId: definition.customerId, clusterId: definition.clusterId, releaseId: definitionId }
          : null;
    } catch {
      if (!stillCurrent()) return;
      resolvedScope.value = null;
    }
  }

  watch(
    () => [options.routeScopeKey(), options.storeDefinitionId()] as const,
    () => {
      if (options.routeScope()) {
        resolvedScope.value = null;
        return;
      }
      const definitionId = options.storeDefinitionId();
      if (!definitionId) {
        resolvedScope.value = null;
        return;
      }
      void resolve(definitionId);
    },
    { immediate: true },
  );

  return { releaseScope };
}
