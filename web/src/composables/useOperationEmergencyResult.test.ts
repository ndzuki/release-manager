import { flushPromises } from '@vue/test-utils';
import { effectScope, nextTick, ref, type EffectScope } from 'vue';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { EffectObservationStatus } from './useEmergencyEffectObservation';
import type { EmergencyResultDisplay } from '@/features/emergency/model';
import { useOperationEmergencyResult } from './useOperationEmergencyResult';

/*
 * The EMERGENCY result panel restores from GetOperation alone and re-fetches when
 * the authoritative stateVersion moves (AC-058-20/22). The effect observation is
 * tied to the same lifecycle: start while the operation is an EMERGENCY, stop when
 * it stops being one. These cases pin that seam, including the late-response guard
 * the page used to own inline.
 */

function emergencyResult(): EmergencyResultDisplay {
  return {
    opType: 'SET_REPLICAS',
    convergencePolicy: 'REQUIRE_PROMOTION',
    requested: true,
    before: null,
    after: null,
    effectStatus: 'APPLIED',
    convergenceTasks: [],
    revertStatus: '',
    reconciledByOperationId: '',
  };
}

function fakeObservation() {
  return {
    status: ref<EffectObservationStatus>('idle'),
    // Typed implementations so the fake satisfies EmergencyObservation; the mocks
    // still record calls for the assertions below.
    start: vi.fn<(operationId: string, liveUpdatesEnabled?: () => boolean) => void>(),
    stop: vi.fn<() => void>(),
  };
}

type FakeObservation = ReturnType<typeof fakeObservation>;

interface Harness {
  api: ReturnType<typeof useOperationEmergencyResult>;
  operationId: ReturnType<typeof ref<string>>;
  isEmergency: ReturnType<typeof ref<boolean>>;
  stateVersion: ReturnType<typeof ref<bigint | undefined>>;
  observation: FakeObservation;
  fetchResult: ReturnType<typeof vi.fn>;
  scope: EffectScope;
}

function setup(
  fetchResult: (operationId: string) => Promise<EmergencyResultDisplay | null> = vi.fn(async () => emergencyResult()),
  isEmergency = true,
): Harness {
  const operationId = ref('op-1');
  const emergency = ref(isEmergency);
  const stateVersion = ref<bigint | undefined>(1n);
  const observation = fakeObservation();
  const fetch = vi.fn(fetchResult);
  const scope = effectScope();
  const api = scope.run(() =>
    useOperationEmergencyResult({
      operationId: () => operationId.value,
      isEmergency: () => emergency.value,
      stateVersion: () => stateVersion.value,
      liveUpdatesEnabled: () => true,
      fetchResult: fetch,
      observation,
    }),
  )!;
  return { api, operationId, isEmergency: emergency, stateVersion, observation, fetchResult: fetch, scope };
}

let harness: Harness | null = null;
afterEach(() => {
  harness?.scope.stop();
  harness = null;
});

describe('useOperationEmergencyResult', () => {
  it('starts observing and loads the result for an EMERGENCY operation', async () => {
    harness = setup();
    await flushPromises();

    expect(harness.fetchResult).toHaveBeenCalledWith('op-1');
    expect(harness.api.result.value).toEqual(emergencyResult());
    expect(harness.observation.start).toHaveBeenCalledWith('op-1', expect.any(Function));
  });

  it('does not observe a non-emergency operation', async () => {
    harness = setup(undefined, false);
    await flushPromises();

    expect(harness.fetchResult).not.toHaveBeenCalled();
    expect(harness.observation.start).not.toHaveBeenCalled();
    expect(harness.observation.stop).toHaveBeenCalled();
    expect(harness.api.result.value).toBeNull();
  });

  it('stops the observation when the operation stops being an EMERGENCY', async () => {
    harness = setup();
    await flushPromises();

    harness.isEmergency.value = false;
    await nextTick();

    expect(harness.observation.stop).toHaveBeenCalled();
    expect(harness.api.result.value).toBeNull();
  });

  it('re-reads only when the state version actually moves', async () => {
    harness = setup();
    await flushPromises();
    expect(harness.fetchResult).toHaveBeenCalledTimes(1);

    harness.stateVersion.value = 2n;
    await nextTick();
    await flushPromises();
    expect(harness.fetchResult).toHaveBeenCalledTimes(2);

    // Re-assigning the same version is not movement: no extra read.
    harness.stateVersion.value = 2n;
    await nextTick();
    expect(harness.fetchResult).toHaveBeenCalledTimes(2);
  });

  it('drops a late result for an operation the page has left', async () => {
    let resolveFirst!: (value: EmergencyResultDisplay) => void;
    harness = setup(() => new Promise((resolve) => {
      // Keep the FIRST read's resolver: the second read (for the new operation)
      // must stay in flight so only the stale one can settle.
      resolveFirst ??= resolve;
    }));

    harness.operationId.value = 'op-2';
    harness.isEmergency.value = true;
    await nextTick();

    resolveFirst(emergencyResult());
    await flushPromises();

    expect(harness.api.result.value).toBeNull();
  });
});
