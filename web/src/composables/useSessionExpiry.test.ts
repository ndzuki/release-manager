import { createApp, nextTick, ref, type App, type Ref } from 'vue';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { SESSION_EXPIRY_MAX_DELAY_MS, useSessionExpiry } from './useSessionExpiry';

/*
 * UX-055. The composable schedules the logout, and both ends of the delay arithmetic
 * are load-bearing: a delay below zero must expire, and a delay above
 * SESSION_EXPIRY_MAX_DELAY_MS must NOT be handed to `setTimeout` as-is, because the
 * platform clamps it to 1ms (measured 2026-10-17 in Node: "Timeout duration was set to
 * 1", fired after 2ms). The tests below pin the two ways a "clamp" can be wrong — the
 * original overflow, and a clamp that truncates the wait and lets the session live on.
 *
 * The composable reads lifecycle hooks (`watch(..., { immediate: true })`,
 * `onScopeDispose`), so it is hosted in a real app the way `useOperatorPolling` is.
 */
const DAY_MS = 24 * 60 * 60 * 1_000;
const THIRTY_DAYS_MS = 30 * DAY_MS;

function withSessionExpiry(expiresAt: Ref<number | null>, onExpired: () => void): App {
  const app = createApp({
    setup() {
      useSessionExpiry({ expiresAt, onExpired });
      return () => null;
    },
  });
  app.mount(document.createElement('div'));
  return app;
}

function secondsFromNow(ms: number, baseSeconds: number): number {
  return baseSeconds + ms / 1_000;
}

describe('useSessionExpiry', () => {
  let app: App | undefined;

  beforeEach(() => {
    vi.useFakeTimers();
    // Midnight with no milliseconds, so every deadline below lands on a whole second and
    // the assertions can be exact instead of tolerance-based.
    vi.setSystemTime(new Date('2026-10-17T00:00:00.000Z'));
  });

  afterEach(() => {
    app?.unmount();
    app = undefined;
    vi.useRealTimers();
  });

  it('expires a normal session at its deadline, not before', async () => {
    const baseSeconds = Math.floor(Date.now() / 1_000);
    const expiresAt = ref<number | null>(secondsFromNow(15 * 60 * 1_000, baseSeconds));
    const onExpired = vi.fn();
    app = withSessionExpiry(expiresAt, onExpired);
    await nextTick();

    await vi.advanceTimersByTimeAsync(15 * 60 * 1_000 - 1);
    expect(onExpired).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(1);
    expect(onExpired).toHaveBeenCalledTimes(1);
  });

  /*
   * The regression itself. 30 days is the smallest round case above the ceiling
   * (2_592_000_000ms vs 2_147_483_647ms): the old single `setTimeout` clamped it to 2ms
   * and logged the user out right after login.
   */
  it('does not expire a >24.8-day session early (UX-055)', async () => {
    expect(THIRTY_DAYS_MS).toBeGreaterThan(SESSION_EXPIRY_MAX_DELAY_MS);
    const baseSeconds = Math.floor(Date.now() / 1_000);
    const expiresAt = ref<number | null>(secondsFromNow(THIRTY_DAYS_MS, baseSeconds));
    const onExpired = vi.fn();
    app = withSessionExpiry(expiresAt, onExpired);
    await nextTick();

    // The overflow fired at ~1-2ms; a session 30 days out must survive that window.
    await vi.advanceTimersByTimeAsync(2);
    expect(onExpired).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(60_000);
    expect(onExpired).not.toHaveBeenCalled();
  });

  /*
   * The other half of the fix: clamping must not become "never log out". A truncating
   * fix fires `onExpired` when the ceiling elapses (24.86 days in, ~5.2 days early),
   * which both assertions below reject.
   */
  it('still expires the >24.8-day session exactly at its deadline (UX-055)', async () => {
    const baseSeconds = Math.floor(Date.now() / 1_000);
    const expiresAt = ref<number | null>(secondsFromNow(THIRTY_DAYS_MS, baseSeconds));
    const onExpired = vi.fn();
    app = withSessionExpiry(expiresAt, onExpired);
    await nextTick();

    // One full ceiling elapses and re-arms: the session is still ~5.2 days out.
    await vi.advanceTimersByTimeAsync(SESSION_EXPIRY_MAX_DELAY_MS);
    expect(onExpired).not.toHaveBeenCalled();

    // A truncating clamp would already have logged out by now; the real deadline has not
    // arrived, so the session must still be alive one millisecond before it.
    await vi.advanceTimersByTimeAsync(THIRTY_DAYS_MS - SESSION_EXPIRY_MAX_DELAY_MS - 1);
    expect(onExpired).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(1);
    expect(onExpired).toHaveBeenCalledTimes(1);
  });

  it('expires an already-elapsed session on the next macrotask, not synchronously', async () => {
    const baseSeconds = Math.floor(Date.now() / 1_000);
    const expiresAt = ref<number | null>(baseSeconds - 1);
    const onExpired = vi.fn();
    app = withSessionExpiry(expiresAt, onExpired);
    await nextTick();

    expect(onExpired).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(0);
    expect(onExpired).toHaveBeenCalledTimes(1);
  });

  it('re-arms to the new deadline when the expiry moves, and clears the old timer', async () => {
    const baseSeconds = Math.floor(Date.now() / 1_000);
    const expiresAt = ref<number | null>(secondsFromNow(60_000, baseSeconds));
    const onExpired = vi.fn();
    app = withSessionExpiry(expiresAt, onExpired);
    await nextTick();

    await vi.advanceTimersByTimeAsync(30_000);
    expiresAt.value = secondsFromNow(120_000, baseSeconds);
    await nextTick();

    await vi.advanceTimersByTimeAsync(120_000 - 30_000 - 1);
    expect(onExpired).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(1);
    expect(onExpired).toHaveBeenCalledTimes(1);

    // The superseded 60s timer must have been cleared: were it still armed it would fire
    // here and make the count two.
    await vi.advanceTimersByTimeAsync(60_000);
    expect(onExpired).toHaveBeenCalledTimes(1);
  });

  it('stops the timer when the scope is disposed', async () => {
    const baseSeconds = Math.floor(Date.now() / 1_000);
    const expiresAt = ref<number | null>(secondsFromNow(60_000, baseSeconds));
    const onExpired = vi.fn();
    app = withSessionExpiry(expiresAt, onExpired);
    await nextTick();

    app.unmount();
    app = undefined;

    await vi.advanceTimersByTimeAsync(60_000);
    expect(onExpired).not.toHaveBeenCalled();
  });
});
