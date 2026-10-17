import { onScopeDispose, watch } from 'vue';
import type { Ref } from 'vue';

type SessionExpiryTimer = number | undefined;

interface SessionExpiryOptions {
  expiresAt: Ref<number | null>;
  onExpired: () => void;
}

/*
 * The longest delay `setTimeout` accepts. A larger value does not wait longer: the
 * platform stores it in a 32-bit signed integer, warns "Timeout duration was set to 1"
 * and fires after ~1ms (measured 2026-10-17 in Node; browsers clamp the same way), so a
 * session whose remaining time exceeds this used to be logged out instantly (UX-055).
 */
export const SESSION_EXPIRY_MAX_DELAY_MS = 2 ** 31 - 1;

export function useSessionExpiry(options: SessionExpiryOptions): void {
  let timer: SessionExpiryTimer;

  /** Whole milliseconds left, recomputed on every wake so a clock jump cannot skew it. */
  function remainingMs(expiresAt: number): number {
    return expiresAt * 1_000 - Date.now();
  }

  /*
   * Sleeps in segments capped at SESSION_EXPIRY_MAX_DELAY_MS and re-arms until the
   * deadline really arrives. Both halves are load-bearing:
   *
   *  - The upper clamp is what keeps a long-lived session from being clamped to 1ms by
   *    the platform.
   *  - Reaching the clamp is NOT the expiry: every wake recomputes the remaining time,
   *    so a session longer than the cap is slept across several timers and still expires
   *    on time. Treating the cap as the deadline logs the session out ~5 days early, and
   *    re-arming without ever checking the deadline means it never logs out at all; the
   *    deadline check is what makes the clamp safe.
   *
   * An already-elapsed expiry keeps the old shape: a 0ms timer, so `onExpired` runs on
   * the next macrotask rather than synchronously inside the watcher callback.
   */
  function arm(expiresAt: number): void {
    const remaining = remainingMs(expiresAt);
    timer = window.setTimeout(
      () => {
        if (remainingMs(expiresAt) > 0) arm(expiresAt);
        else options.onExpired();
      },
      Math.min(Math.max(0, remaining), SESSION_EXPIRY_MAX_DELAY_MS),
    );
  }

  watch(
    options.expiresAt,
    (expiresAt) => {
      clearTimeout(timer);
      if (!expiresAt) return;
      arm(expiresAt);
    },
    { immediate: true },
  );

  onScopeDispose(() => clearTimeout(timer));
}
