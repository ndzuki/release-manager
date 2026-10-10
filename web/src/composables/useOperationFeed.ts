import { computed, ref } from 'vue';
import {
  isMaintenanceError,
  listNonTerminalOperations,
  mapOperationError,
  type NonTerminalOperationItem,
} from '@/connect/operation-api';
import { correlationLine, describeError } from '@/connect/error-copy';

/*
 * The cross-release non-terminal feed's state machine (TASK-277).
 *
 * Two surfaces read the same RPC (the Operation centre and the home "waiting on me"
 * summary), so the mapping from a failed call to a RENDERABLE state lives here once.
 *
 * Two outcomes are deliberately NOT folded into `error`, because the error state's
 * only advice is "retry" and that is wrong for both:
 *   - maintenance: the orchestrator refuses this RPC while it is in maintenance mode
 *     (the procedure is not in orchestratorReadOnlyProcedures), and no retry can work
 *     until the cutover ends;
 *   - forbidden: PERMISSION_DENIED, which is the authorization refusal on the read
 *     (`release:read` denied by casbin, or an inactive membership). Neither surface
 *     using this composable sends `customerId`, so the "customer outside your active
 *     bindings" answer TASK-276 defines is NOT reachable from this UI. Repeating the
 *     same request cannot succeed either way.
 * Loading / empty / rows stay exactly the DataTable contract.
 */
export interface OperationFeedError {
  message: string;
  details: string;
  retryable: boolean;
}

export interface UseOperationFeedOptions {
  /** Sent as page_size; 0/omitted lets the server apply its default (20, capped at 100). */
  pageSize?: number;
}

export function useOperationFeed(options: UseOperationFeedOptions = {}) {
  const pageSize = options.pageSize ?? 0;

  const items = ref<NonTerminalOperationItem[]>([]);
  const nextPageToken = ref('');
  const loading = ref(false);
  const error = ref<OperationFeedError | null>(null);
  const maintenance = ref(false);
  const forbidden = ref(false);

  const empty = computed(
    () =>
      !loading.value &&
      !error.value &&
      !maintenance.value &&
      !forbidden.value &&
      items.value.length === 0,
  );

  /**
   * Fetches ONE page and REPLACES what is on screen. `pageToken` is the cursor the
   * caller carries; the empty string asks for the first page. A previous failure must
   * never leak into the next outcome, so all four state flags are reset up front.
   */
  async function load(pageToken = ''): Promise<void> {
    loading.value = true;
    error.value = null;
    maintenance.value = false;
    forbidden.value = false;
    try {
      const page = await listNonTerminalOperations({ pageSize, pageToken });
      items.value = page.operations;
      nextPageToken.value = page.nextPageToken;
    } catch (requestError) {
      // Nothing on screen belongs to a failed query: an expired or rejected cursor
      // must not leave the previous page's rows under a new request.
      items.value = [];
      nextPageToken.value = '';
      if (isMaintenanceError(requestError)) {
        maintenance.value = true;
      } else {
        const mapped = mapOperationError(requestError);
        if (mapped.code === 'permission_denied') {
          forbidden.value = true;
        } else {
          error.value = {
            message: mapped.message,
            details: correlationLine(describeError(requestError)),
            retryable: mapped.retryable,
          };
        }
      }
    } finally {
      loading.value = false;
    }
  }

  return { items, nextPageToken, loading, error, maintenance, forbidden, empty, load };
}
