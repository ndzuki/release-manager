import { ref, watch, type Ref } from 'vue';
import { getEmergencyResult } from '@/connect/emergency-api';
import {
  useEmergencyEffectObservation,
  type EffectObservationStatus,
} from '@/composables/useEmergencyEffectObservation';
import type { EmergencyResultDisplay } from '@/features/emergency/model';

/*
 * Emergency result observation (TASK-058 Step 5 seam). The EmergencyResultPanel
 * restores the full result from GetOperation alone (AC-058-20) and re-fetches
 * whenever the authoritative stateVersion moves (late Result resolution increments
 * it, AC-058-22). The UNKNOWN observation reuses the operationTimeline watch
 * lifecycle (D3/D10).
 */

/** The subset of useEmergencyEffectObservation this composable drives. */
export interface EmergencyObservation {
  status: Ref<EffectObservationStatus>;
  start: (operationId: string, liveUpdatesEnabled?: () => boolean) => void;
  stop: () => void;
}

export interface OperationEmergencyResultOptions {
  operationId: () => string;
  isEmergency: () => boolean;
  stateVersion: () => bigint | undefined;
  liveUpdatesEnabled: () => boolean;
  /** Test seam: replaces the GetOperation read. */
  fetchResult?: (operationId: string) => Promise<EmergencyResultDisplay | null>;
  /** Test seam: replaces the timeline-backed effect observation. */
  observation?: EmergencyObservation;
}

export interface OperationEmergencyResult {
  result: Ref<EmergencyResultDisplay | null>;
  observationStatus: Ref<EffectObservationStatus>;
  /** Tears the observation down; the page does this before resetting the store. */
  stopObservation: () => void;
}

export function useOperationEmergencyResult(
  options: OperationEmergencyResultOptions,
): OperationEmergencyResult {
  const fetchResult = options.fetchResult ?? getEmergencyResult;
  const observation = options.observation ?? useEmergencyEffectObservation();
  const result = ref<EmergencyResultDisplay | null>(null);

  async function reload(): Promise<void> {
    const currentId = options.operationId();
    if (!currentId || !options.isEmergency()) return;
    try {
      const read = await fetchResult(currentId);
      if (options.operationId() !== currentId) return;
      result.value = read;
    } catch {
      // The summary/timeline already surface stream errors; the panel simply
      // keeps the last authoritative result when the fetch fails transiently.
    }
  }

  watch(
    () => [options.operationId(), options.isEmergency()] as const,
    () => {
      result.value = null;
      if (options.isEmergency()) {
        void reload();
        observation.start(options.operationId(), options.liveUpdatesEnabled);
      } else {
        observation.stop();
      }
    },
    { immediate: true },
  );

  watch(options.stateVersion, (current, previous) => {
    if (options.isEmergency() && current !== previous) {
      void reload();
    }
  });

  return {
    result,
    observationStatus: observation.status,
    stopObservation: observation.stop,
  };
}
