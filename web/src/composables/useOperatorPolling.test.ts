import { createApp, shallowRef, type App } from 'vue';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { operatorPollingIntervalMs, useOperatorPolling } from './useOperatorPolling';

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

  it('stops automatic retries after a failed refresh and clears timers on unmount', async () => {
    const refresh = vi.fn().mockResolvedValue(false);
    app = withPolling(refresh);

    await vi.advanceTimersByTimeAsync(30_000);

    expect(refresh).toHaveBeenCalledTimes(1);
    app.unmount();
    app = undefined;
    expect(vi.getTimerCount()).toBe(0);
  });
});
