// Emergency effect observation composable (plan v3 Step 3 / decisions D3+D10).
//
// Wraps the operationTimeline store, which already owns the WatchOperation
// stream lifecycle: snapshot/replay, exponential backoff reconnect
// (RECONNECT_BASE_MS=1000, RECONNECT_MAX_MS=30000, RECONNECT_JITTER=0.2),
// EMERGENCY_EFFECT_RESOLVED handling and scope fencing. This composable adds
// only the stop-on-resolution semantics and an injectable seam for tests:
// start()/stop() are the entire public surface (plan Step 3).
import { getCurrentInstance, onUnmounted, ref, unref, watch, type Ref, type WatchStopHandle } from 'vue';
import { useOperationTimelineStore } from '@/stores/operationTimeline';

export type EffectObservationStatus = 'idle' | 'watching' | 'resolved' | 'not_applicable';

type TimelineEffectStatus = 'watching' | 'resolved' | 'not_applicable';

export interface EffectObservationTimeline {
  configure: (options: { liveUpdatesEnabled?: () => boolean }) => void;
  load: (operationId: string) => Promise<void>;
  reset: () => void;
  // Pinia setup stores unwrap refs on the instance (plain value); test fakes
  // may hand over a Ref — unref() normalizes both.
  emergencyEffectStatus: TimelineEffectStatus | Ref<TimelineEffectStatus>;
}

export interface EffectObservationOptions {
  /** Test seam: replace the underlying timeline store handle. */
  timeline?: EffectObservationTimeline;
}

export function useEmergencyEffectObservation(options: EffectObservationOptions = {}) {
  const timeline = options.timeline ?? useOperationTimelineStore();
  const status = ref<EffectObservationStatus>('idle');

  let stopWatch: WatchStopHandle | null = null;
  let currentOperationId: string | null = null;

  function stopWatching(): void {
    if (stopWatch) {
      stopWatch();
      stopWatch = null;
    }
  }

  function start(operationId: string, liveUpdatesEnabled?: () => boolean): void {
    stopWatching();
    currentOperationId = operationId;
    if (liveUpdatesEnabled) timeline.configure({ liveUpdatesEnabled });
    void timeline.load(operationId);
    stopWatch = watch(
      () => unref(timeline.emergencyEffectStatus),
      (next) => {
        switch (next) {
          case 'watching':
            status.value = 'watching';
            break;
          case 'resolved':
            // Final snapshot / EMERGENCY_EFFECT_RESOLVED entry processed:
            // stop observing (AC-058-23).
            status.value = 'resolved';
            break;
          case 'not_applicable':
            status.value = 'not_applicable';
            break;
          default:
            status.value = 'idle';
        }
      },
      { immediate: true, flush: 'sync' },
    );
  }

  function stop(): void {
    stopWatching();
    if (currentOperationId !== null) {
      timeline.reset();
      currentOperationId = null;
    }
    status.value = 'idle';
  }

  // Unmount/scope change: abort the stream and reset volatile state
  // (REQ-058 page lifecycle; the page also calls stop() on route param change).
  // Guarded so the composable stays usable in store-only/test contexts.
  if (getCurrentInstance()) {
    onUnmounted(stop);
  }

  return { status, start, stop };
}
