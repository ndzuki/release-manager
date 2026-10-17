import { onMounted, onUnmounted, watch, type Ref } from 'vue';

interface UseOperatorPollingOptions {
  heartbeatIntervalSeconds: Ref<number | null>;
  refresh: () => Promise<boolean>;
}

/** The steady-state poll interval derived from the server heartbeat. */
export function operatorPollingIntervalMs(heartbeatIntervalSeconds: number): number {
  return Math.min(300_000, Math.max(10_000, heartbeatIntervalSeconds * 2_000));
}

/*
 * TASK-281: a single failed refresh used to stop the loop for good
 * (`refreshAndSchedule` only scheduled when `refresh()` resolved `true`). A transient
 * failure — a network blip, one 5xx — is normal for a long-running operator status, so
 * failures now retry with exponential backoff. The chain is bounded: once
 * MAX_CONSECUTIVE_REFRESH_FAILURES failures pile up the automatic timer stops (a
 * user-driven focus/visibility refresh may still restart it, and a success resets the
 * budget), so a persistently broken endpoint cannot poll forever.
 *
 * No jitter, deliberately: one console tab polls one visible, focused cluster scope at a
 * time; the steady interval is already >= 10s and the retry chain is at most 5 attempts,
 * so there is no observed thundering-herd to spread. Jitter would only make the backoff
 * non-deterministic and its tests weaker.
 */
export const MAX_CONSECUTIVE_REFRESH_FAILURES = 5;
export const OPERATOR_POLLING_RETRY_BASE_MS = 5_000;
export const OPERATOR_POLLING_RETRY_MAX_MS = 60_000;

/** Backoff before attempt N+1 after N consecutive failures (capped defensively). */
export function operatorPollingRetryDelayMs(consecutiveFailures: number): number {
  if (consecutiveFailures <= 0) return 0;
  return Math.min(
    OPERATOR_POLLING_RETRY_MAX_MS,
    OPERATOR_POLLING_RETRY_BASE_MS * 2 ** (consecutiveFailures - 1),
  );
}

export function useOperatorPolling(options: UseOperatorPollingOptions): void {
  let timer: number | undefined;
  let active = false;
  let refreshing = false;
  let consecutiveFailures = 0;

  function clearTimer(): void {
    if (timer === undefined) return;
    window.clearTimeout(timer);
    timer = undefined;
  }

  function canPoll(): boolean {
    return active && document.visibilityState === 'visible' && document.hasFocus();
  }

  function schedule(): void {
    clearTimer();
    const heartbeat = options.heartbeatIntervalSeconds.value;
    if (!canPoll() || heartbeat === null || heartbeat <= 0) return;
    // The retry budget is spent: the timer chain stops here. A success resets the
    // counter, and focus/visibility still triggers one user-driven attempt.
    if (consecutiveFailures >= MAX_CONSECUTIVE_REFRESH_FAILURES) return;
    const delay = consecutiveFailures === 0
      ? operatorPollingIntervalMs(heartbeat)
      : operatorPollingRetryDelayMs(consecutiveFailures);
    timer = window.setTimeout(() => void refreshAndSchedule(), delay);
  }

  async function refreshAndSchedule(): Promise<void> {
    clearTimer();
    if (!canPoll() || refreshing) return;
    refreshing = true;
    let succeeded = false;
    try {
      succeeded = await options.refresh();
    } catch {
      // A throwing refresh is a failure like any other, not an unhandled rejection.
      succeeded = false;
    } finally {
      refreshing = false;
      consecutiveFailures = succeeded ? 0 : consecutiveFailures + 1;
      schedule();
    }
  }

  function handleVisibilityChange(): void {
    if (document.visibilityState === 'visible') void refreshAndSchedule();
    else clearTimer();
  }

  function handleFocus(): void {
    void refreshAndSchedule();
  }

  watch(options.heartbeatIntervalSeconds, schedule);

  onMounted(() => {
    active = true;
    document.addEventListener('visibilitychange', handleVisibilityChange);
    window.addEventListener('focus', handleFocus);
    window.addEventListener('blur', clearTimer);
    schedule();
  });

  onUnmounted(() => {
    active = false;
    clearTimer();
    document.removeEventListener('visibilitychange', handleVisibilityChange);
    window.removeEventListener('focus', handleFocus);
    window.removeEventListener('blur', clearTimer);
  });
}
