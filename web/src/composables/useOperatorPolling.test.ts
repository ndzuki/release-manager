import { createApp, shallowRef, type App } from 'vue';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  MAX_CONSECUTIVE_REFRESH_FAILURES,
  OPERATOR_POLLING_RETRY_BASE_MS,
  OPERATOR_POLLING_RETRY_MAX_MS,
  operatorPollingIntervalMs,
  operatorPollingRetryDelayMs,
  useOperatorPolling,
} from './useOperatorPolling';

type PollingHandle = ReturnType<typeof useOperatorPolling>;

function withPolling(
  refresh: () => Promise<boolean>,
  heartbeat = 15,
  onReady?: (handle: PollingHandle) => void,
): App {
  const app = createApp({
    setup() {
      const handle = useOperatorPolling({
        heartbeatIntervalSeconds: shallowRef(heartbeat),
        refresh,
      });
      onReady?.(handle);
      return () => null;
    },
  });
  app.mount(document.createElement('div'));
  return app;
}

describe('operator polling', () => {
  let app: App | undefined;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.spyOn(document, 'hasFocus').mockReturnValue(true);
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
  });

  afterEach(() => {
    app?.unmount();
    app = undefined;
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it.each([
    [1, 10_000],
    [15, 30_000],
    [200, 300_000],
  ])('clamps heartbeat %s seconds to %s ms', (heartbeat, expected) => {
    expect(operatorPollingIntervalMs(heartbeat)).toBe(expected);
  });

  it.each([
    [0, 0],
    [1, OPERATOR_POLLING_RETRY_BASE_MS],
    [2, OPERATOR_POLLING_RETRY_BASE_MS * 2],
    [3, OPERATOR_POLLING_RETRY_BASE_MS * 4],
    [4, OPERATOR_POLLING_RETRY_BASE_MS * 8],
    [20, OPERATOR_POLLING_RETRY_MAX_MS],
  ])('backs off %s consecutive failures to %s ms', (failures, expected) => {
    expect(operatorPollingRetryDelayMs(failures)).toBe(expected);
  });

  it('pauses while hidden and refreshes immediately after becoming visible', async () => {
    const refresh = vi.fn().mockResolvedValue(true);
    const clearTimeout = vi.spyOn(window, 'clearTimeout');
    app = withPolling(refresh);

    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' });
    document.dispatchEvent(new Event('visibilitychange'));

    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
    document.dispatchEvent(new Event('visibilitychange'));
    await Promise.resolve();

    expect(refresh).toHaveBeenCalledTimes(1);
    app.unmount();
    app = undefined;
    expect(clearTimeout).toHaveBeenCalled();
  });

  // TASK-281: the old loop stopped for good on the first failure. A transient
  // failure must retry (bounded), and only the exhausted budget may stop the chain.
  it('retries after a failed refresh instead of stopping', async () => {
    const refresh = vi.fn().mockResolvedValue(false);
    app = withPolling(refresh);

    // The steady interval elapses into the first attempt…
    await vi.advanceTimersByTimeAsync(30_000);
    expect(refresh).toHaveBeenCalledTimes(1);

    // …and the failure schedules the base backoff rather than ending the loop.
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS);
    expect(refresh).toHaveBeenCalledTimes(2);
  });

  it('widens the backoff with each consecutive failure', async () => {
    const refresh = vi.fn().mockResolvedValue(false);
    app = withPolling(refresh);

    await vi.advanceTimersByTimeAsync(30_000); // attempt 1, failures=1 → 5s
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS); // attempt 2, failures=2 → 10s
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 2 - 1);
    expect(refresh).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(refresh).toHaveBeenCalledTimes(3);
  });

  it('stops after the bounded number of consecutive failures and clears timers on unmount', async () => {
    const refresh = vi.fn().mockResolvedValue(false);
    app = withPolling(refresh);

    // Exhaust the automatic chain: the steady interval plus 5s/10s/20s/40s backoffs.
    await vi.advanceTimersByTimeAsync(30_000);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 2);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 4);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 8);

    expect(refresh).toHaveBeenCalledTimes(MAX_CONSECUTIVE_REFRESH_FAILURES);
    // Bounded, not endless: nothing is scheduled any more.
    expect(vi.getTimerCount()).toBe(0);
    await vi.advanceTimersByTimeAsync(300_000);
    expect(refresh).toHaveBeenCalledTimes(MAX_CONSECUTIVE_REFRESH_FAILURES);

    app.unmount();
    app = undefined;
    expect(vi.getTimerCount()).toBe(0);
  });

  it('resets the backoff budget after a successful refresh', async () => {
    const refresh = vi.fn().mockResolvedValueOnce(false).mockResolvedValue(true);
    app = withPolling(refresh);

    await vi.advanceTimersByTimeAsync(30_000); // attempt 1 fails, failures=1
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS); // attempt 2 succeeds, resets
    await vi.advanceTimersByTimeAsync(30_000); // attempt 3 on the steady interval again
    expect(refresh).toHaveBeenCalledTimes(3);
  });

  /*
   * A refresh that rejects is a failure like a resolved `false`, not an unhandled
   * rejection that silently kills the loop.
   *
   * The unhandled-rejection listener is what actually pins the `catch`: without it the
   * throw still falls through to `finally`, so counting assertions alone stay green and
   * only the process exit code notices. The delay boundary (nothing at base-1, attempt 2
   * at base) pins "counted as failure" more tightly than "called twice" would.
   */
  it('treats a throwing refresh as a retryable failure without an unhandled rejection', async () => {
    const unhandled: unknown[] = [];
    const onUnhandled = (reason: unknown): void => {
      unhandled.push(reason);
    };
    process.on('unhandledRejection', onUnhandled);
    try {
      const refresh = vi.fn().mockRejectedValue(new Error('boom'));
      app = withPolling(refresh);

      // The first automatic attempt rejects; the scheduler must absorb it.
      await vi.advanceTimersByTimeAsync(30_000);
      // Node emits unhandledRejection a turn after the rejection itself.
      await vi.advanceTimersByTimeAsync(0);
      expect(unhandled).toEqual([]);
      expect(refresh).toHaveBeenCalledTimes(1);

      // …and the failure is counted exactly like a resolved `false`: the base backoff
      // is what schedules attempt 2, not merely "some timer".
      await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS - 1);
      expect(refresh).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(1);
      expect(refresh).toHaveBeenCalledTimes(2);
      await vi.advanceTimersByTimeAsync(0);
      expect(unhandled).toEqual([]);
    } finally {
      process.off('unhandledRejection', onUnhandled);
    }
  });

  // The throwing path must also spend the same bounded budget as a resolved `false`,
  // so the cap keeps holding when the endpoint is broken rather than merely flaky.
  it('stops a persistently throwing refresh at the same bounded budget', async () => {
    const refresh = vi.fn().mockRejectedValue(new Error('boom'));
    app = withPolling(refresh);

    await vi.advanceTimersByTimeAsync(30_000);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 2);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 4);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 8);

    expect(refresh).toHaveBeenCalledTimes(MAX_CONSECUTIVE_REFRESH_FAILURES);
    expect(vi.getTimerCount()).toBe(0);
  });

  /*
   * TASK-325 (AC-325-05): the stop is a contract with the UI. `stopped` must become true
   * exactly when the chain is spent, and must stay false for the deliberate pauses —
   * reporting a background tab or an unfocused window as "stopped" would tell every
   * screen-reader user that the data froze whenever they switch tabs.
   */
  async function exhaustRetryBudget(): Promise<void> {
    await vi.advanceTimersByTimeAsync(30_000); // attempt 1, failures=1
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 2);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 4);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 8);
  }

  it('exposes the stopped state only once the retry budget is spent', async () => {
    const refresh = vi.fn().mockResolvedValue(false);
    let handle: PollingHandle | undefined;
    app = withPolling(refresh, 15, (next) => { handle = next; });

    expect(handle?.stopped.value).toBe(false);

    await vi.advanceTimersByTimeAsync(30_000); // failures=1
    expect(handle?.stopped.value).toBe(false);

    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 2);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 4);
    expect(handle?.stopped.value).toBe(false);

    // The fifth failure spends the budget; the chain is spent and the page must say so.
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS * 8);
    expect(refresh).toHaveBeenCalledTimes(MAX_CONSECUTIVE_REFRESH_FAILURES);
    expect(handle?.stopped.value).toBe(true);
  });

  it('keeps a hidden tab and a blurred window out of the stopped state', async () => {
    const refresh = vi.fn().mockResolvedValue(true);
    let handle: PollingHandle | undefined;
    app = withPolling(refresh, 15, (next) => { handle = next; });

    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' });
    document.dispatchEvent(new Event('visibilitychange'));
    expect(handle?.stopped.value).toBe(false);

    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
    document.dispatchEvent(new Event('visibilitychange'));
    await vi.advanceTimersByTimeAsync(0);
    expect(refresh).toHaveBeenCalledTimes(1);

    window.dispatchEvent(new Event('blur'));
    vi.spyOn(document, 'hasFocus').mockReturnValue(false);
    // The pause really paused — and it is still not a stop.
    await vi.advanceTimersByTimeAsync(300_000);
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(handle?.stopped.value).toBe(false);
  });

  it('clears the stopped state and restores the automatic chain when resumed', async () => {
    const refresh = vi.fn().mockResolvedValue(false);
    let handle: PollingHandle | undefined;
    app = withPolling(refresh, 15, (next) => { handle = next; });

    await exhaustRetryBudget();
    expect(refresh).toHaveBeenCalledTimes(MAX_CONSECUTIVE_REFRESH_FAILURES);
    expect(handle?.stopped.value).toBe(true);

    refresh.mockResolvedValue(true);
    handle?.resume();
    // The budget is dropped before the attempt resolves, so the notice disappears as
    // soon as the user asks rather than after a round trip.
    expect(handle?.stopped.value).toBe(false);

    await vi.advanceTimersByTimeAsync(0);
    expect(refresh).toHaveBeenCalledTimes(MAX_CONSECUTIVE_REFRESH_FAILURES + 1);

    // Genuinely restored, not merely re-labelled: the steady interval polls again.
    await vi.advanceTimersByTimeAsync(30_000);
    expect(refresh).toHaveBeenCalledTimes(MAX_CONSECUTIVE_REFRESH_FAILURES + 2);
    expect(handle?.stopped.value).toBe(false);
  });

  it('clears the stopped state when a user-driven focus refresh succeeds', async () => {
    const refresh = vi.fn().mockResolvedValue(false);
    let handle: PollingHandle | undefined;
    app = withPolling(refresh, 15, (next) => { handle = next; });

    await exhaustRetryBudget();
    expect(handle?.stopped.value).toBe(true);

    // The pre-existing restart path (TASK-281: focus/visibility still triggers one
    // attempt after the budget is spent) must also clear the visible stop.
    refresh.mockResolvedValue(true);
    window.dispatchEvent(new Event('focus'));
    await vi.advanceTimersByTimeAsync(0);
    expect(refresh).toHaveBeenCalledTimes(MAX_CONSECUTIVE_REFRESH_FAILURES + 1);
    expect(handle?.stopped.value).toBe(false);
  });
});
