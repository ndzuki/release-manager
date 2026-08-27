import { computed, ref } from 'vue';
import { describe, expect, it, vi } from 'vitest';
import { useEmergencyEffectObservation } from '@/composables/useEmergencyEffectObservation';

function fakeTimeline() {
  const emergencyEffectStatus = ref<'watching' | 'resolved' | 'not_applicable'>('not_applicable');
  const configure = vi.fn();
  // Mimics production: load() resets the effect observation state before the
  // new stream starts.
  const load = vi.fn(async () => {
    emergencyEffectStatus.value = 'not_applicable';
  });
  const reset = vi.fn();
  return { emergencyEffectStatus, configure, load, reset };
}

describe('useEmergencyEffectObservation (D3/D10 wrapper)', () => {
  it('starts the timeline watch and reflects the underlying status', () => {
    const timeline = fakeTimeline();
    const observer = useEmergencyEffectObservation({ timeline });

    observer.start('op1');
    expect(timeline.load).toHaveBeenCalledWith('op1');
    expect(observer.status.value).toBe('not_applicable');

    timeline.emergencyEffectStatus.value = 'watching';
    expect(observer.status.value).toBe('watching');
  });

  it('marks resolved when the effect resolves and keeps the final state', () => {
    const timeline = fakeTimeline();
    const observer = useEmergencyEffectObservation({ timeline });

    observer.start('op1');
    timeline.emergencyEffectStatus.value = 'watching';
    timeline.emergencyEffectStatus.value = 'resolved';
    expect(observer.status.value).toBe('resolved');
    // No further timeline reset happens just because the effect resolved —
    // the page keeps the final snapshot (AC-058-23).
    expect(timeline.reset).not.toHaveBeenCalled();
  });

  it('stop() resets the timeline and returns to idle', () => {
    const timeline = fakeTimeline();
    const observer = useEmergencyEffectObservation({ timeline });

    observer.start('op1');
    timeline.emergencyEffectStatus.value = 'watching';
    observer.stop();
    expect(timeline.reset).toHaveBeenCalledTimes(1);
    expect(observer.status.value).toBe('idle');
  });

  it('restarting for a new operation supersedes the previous watch', () => {
    const timeline = fakeTimeline();
    const observer = useEmergencyEffectObservation({ timeline });

    observer.start('op1');
    timeline.emergencyEffectStatus.value = 'watching';
    observer.start('op2');
    expect(timeline.load).toHaveBeenLastCalledWith('op2');
    expect(observer.status.value).toBe('not_applicable');
    timeline.emergencyEffectStatus.value = 'resolved';
    expect(observer.status.value).toBe('resolved');
  });

  it('passes the live-updates flag through to the timeline store', () => {
    const timeline = fakeTimeline();
    const observer = useEmergencyEffectObservation({ timeline });
    const liveUpdatesEnabled = computed(() => false);
    observer.start('op1', () => liveUpdatesEnabled.value);
    expect(timeline.configure).toHaveBeenCalledWith({ liveUpdatesEnabled: expect.any(Function) });
  });
});
