import { ref, watch, type Ref } from 'vue';
import { getPreflightResult } from '@/connect/operation-api';
import { correlationLine, describeError } from '@/connect/error-copy';
import { t } from '@/i18n/messages';
import type { PreflightResult } from '@/types/operation';

/*
 * The preflight outcome is part of GetOperation, not of the stream snapshot, so the
 * detail page fetches it alongside the stream and re-fetches when the operation moves
 * on (the coordinator persists it as preflight concludes, AC-056-03).
 *
 * TASK-281: a failed preflight read is NOT the same as "the operation returned no
 * preflight result yet". The failure keeps its own state so the two stay
 * distinguishable, the operator gets a retry entry point, and the correlation line
 * (code · requestId) can be handed to the server logs.
 */

export interface PreflightFailure {
  message: string;
  details: string;
}

export interface OperationPreflightOptions {
  /** The operation currently on screen; '' while the route has none. */
  operationId: () => string;
  /** Route-scope key: changes when the page must forget the previous read. */
  scopeKey: () => string;
  /** Monotonic state version; advancing it means the pipeline may have concluded. */
  stateVersion: () => bigint | undefined;
  /** Test seam: replaces the GetOperation read. */
  fetchResult?: (operationId: string) => Promise<PreflightResult | null>;
}

export interface OperationPreflight {
  result: Ref<PreflightResult | null>;
  failure: Ref<PreflightFailure | null>;
  reload: () => Promise<void>;
}

export function useOperationPreflight(options: OperationPreflightOptions): OperationPreflight {
  const fetchResult = options.fetchResult ?? getPreflightResult;
  const result = ref<PreflightResult | null>(null);
  const failure = ref<PreflightFailure | null>(null);

  async function reload(): Promise<void> {
    const current = options.operationId();
    if (!current) {
      result.value = null;
      failure.value = null;
      return;
    }
    try {
      const read = await fetchResult(current);
      // A late response for a previous operation must not overwrite the current
      // one (same guard the timeline store applies to its own async reads).
      if (options.operationId() === current) {
        result.value = read;
        failure.value = null;
      }
    } catch (error) {
      // The operation and its last_error still render, but the read failure is
      // surfaced rather than swallowed: message plus the correlation line.
      if (options.operationId() === current) {
        result.value = null;
        failure.value = {
          message: t('operation.preflight.loadFailedHint'),
          details: correlationLine(describeError(error)),
        };
      }
    }
  }

  // A new scope starts from a clean slate. Clearing both halves synchronously stops
  // the previous operation's failure banner (or result panel) from rendering while
  // the new scope's read is still in flight.
  watch(
    options.scopeKey,
    () => {
      result.value = null;
      failure.value = null;
      void reload();
    },
    { immediate: true },
  );

  // The coordinator persists the preflight result as the pipeline concludes, so
  // re-read it whenever the operation moves on (AC-056-03).
  watch(() => [options.operationId(), options.stateVersion()] as const, () => void reload());

  return { result, failure, reload };
}
