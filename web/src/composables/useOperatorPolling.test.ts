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

function withPolling(refresh: () => Promise<boolean>, heartbeat = 15): App {
  const app = createApp({
    setup() {
      useOperatorPolling({
        heartbeatIntervalSeconds: shallowRef(heartbeat),
        refresh,
      });
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

  // A refresh that rejects is a failure like a resolved `false`, not an unhandled
  // rejection that silently kills the loop.
  it('treats a throwing refresh as a retryable failure', async () => {
    const refresh = vi.fn().mockRejectedValue(new Error('boom'));
    app = withPolling(refresh);

    await vi.advanceTimersByTimeAsync(30_000);
    await vi.advanceTimersByTimeAsync(OPERATOR_POLLING_RETRY_BASE_MS);
    expect(refresh).toHaveBeenCalledTimes(2);
  });
});
