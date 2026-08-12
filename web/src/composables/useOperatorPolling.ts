import { onMounted, onUnmounted, watch, type Ref } from 'vue';

interface UseOperatorPollingOptions {
  heartbeatIntervalSeconds: Ref<number | null>;
  refresh: () => Promise<boolean>;
}

export function operatorPollingIntervalMs(heartbeatIntervalSeconds: number): number {
  return Math.min(300_000, Math.max(10_000, heartbeatIntervalSeconds * 2_000));
}

export function useOperatorPolling(options: UseOperatorPollingOptions): void {
  let timer: number | undefined;
  let active = false;
  let refreshing = false;

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
    timer = window.setTimeout(() => void refreshAndSchedule(), operatorPollingIntervalMs(heartbeat));
  }

  async function refreshAndSchedule(): Promise<void> {
    clearTimer();
    if (!canPoll() || refreshing) return;
    refreshing = true;
    let succeeded = false;
    try {
      succeeded = await options.refresh();
    } finally {
      refreshing = false;
      if (succeeded) schedule();
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
