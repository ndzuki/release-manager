import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import EmergencyChangeForm from './EmergencyChangeForm.vue';
import type { ConvergencePolicy } from '@/features/emergency/model';

/*
 * AC-058-12 / TASK-325 W3: the reason field mirrors the server's 5-500
 * Unicode-character window and counts in the same unit. The counter is a
 * description of the textarea, so both the number AND the relation (aria-
 * describedby points at the live counter) are asserted here — a counter the
 * screen reader never announces is not the guard this AC asks for.
 */
function mountForm(props: {
  reason?: string;
  convergencePolicy?: ConvergencePolicy;
  requirePromotionAvailable?: boolean;
  mappingComplete?: boolean;
} = {}) {
  return mount(EmergencyChangeForm, {
    props: {
      reason: '',
      convergencePolicy: 'REQUIRE_PROMOTION',
      requirePromotionAvailable: true,
      mappingComplete: true,
      submitError: null,
      ...props,
    },
  });
}

describe('EmergencyChangeForm reason field', () => {
  it('counts Unicode code points and describes the textarea with the counter', () => {
    const wrapper = mountForm({ reason: '🚀🚀🚀🚀🚀' });
    const textarea = wrapper.get('textarea');

    expect(wrapper.get('.char-count').text()).toBe('5 / 500 字符');
    expect(textarea.attributes('aria-describedby')?.split(' ')).toContain('emergency-reason-char-count');
    expect(textarea.attributes('aria-invalid')).toBeUndefined();
  });

  it('shows the 5-character minimum instead of accepting a one-word reason', () => {
    const wrapper = mountForm({ reason: 'abcd' });

    const alert = wrapper.get('[role="alert"]');
    expect(alert.text()).toBe('变更原因至少 5 个字符（当前 4 个）');
    expect(wrapper.get('textarea').attributes('aria-invalid')).toBe('true');
    expect(wrapper.get('textarea').attributes('aria-describedby')).toContain('emergency-reason-char-count');
  });

  it('marks the counter as over the limit and reports the maximum', () => {
    const wrapper = mountForm({ reason: 'a'.repeat(501) });

    expect(wrapper.get('.char-count').classes()).toContain('over');
    expect(wrapper.get('.char-count').text()).toBe('501 / 500 字符');
    expect(wrapper.get('[role="alert"]').text()).toBe('变更原因最多 500 个字符（当前 501 个）');
  });

  it('emits the typed value so the store owns the canonical reason', async () => {
    const wrapper = mountForm();
    await wrapper.get('textarea').setValue('  事故 ID 123  ');
    expect(wrapper.emitted('update:reason')?.at(-1)).toEqual(['  事故 ID 123  ']);
  });
});
