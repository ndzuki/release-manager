import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import EmergencyChangeForm from './EmergencyChangeForm.vue';
import type { ConvergencePolicy } from '@/features/emergency/model';

/*
 * AC-058-12 / TASK-325 W3: the reason field mirrors the server's 1-1000 UTF-8
 * BYTE window and counts in the same unit. The counter is a description of the
 * textarea, so both the number AND the relation (aria-describedby points at the
 * live counter) are asserted here — a counter the screen reader never announces
 * is not the guard this AC asks for.
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
  it('counts UTF-8 bytes and describes the textarea with the counter', () => {
    const wrapper = mountForm({ reason: '🚀🚀🚀🚀🚀' });
    const textarea = wrapper.get('textarea');

    expect(wrapper.get('.char-count').text()).toBe('20 / 1000 字节');
    expect(textarea.attributes('aria-describedby')?.split(' ')).toContain('emergency-reason-char-count');
    expect(textarea.attributes('aria-invalid')).toBeUndefined();
  });

  it('accepts a one-byte reason instead of requiring five characters', () => {
    const wrapper = mountForm({ reason: 'a' });

    expect(wrapper.find('[role="alert"]').exists()).toBe(false);
    expect(wrapper.get('.char-count').text()).toBe('1 / 1000 字节');
    expect(wrapper.get('textarea').attributes('aria-invalid')).toBeUndefined();
  });

  it('rejects a reason that is empty after trimming', () => {
    const wrapper = mountForm({ reason: '   ' });

    const alert = wrapper.get('[role="alert"]');
    expect(alert.text()).toBe('请填写变更原因');
    expect(wrapper.get('textarea').attributes('aria-invalid')).toBe('true');
    expect(wrapper.get('textarea').attributes('aria-describedby')).toContain('emergency-reason-char-count');
  });

  it('marks the counter as over the byte limit and reports the maximum', () => {
    const wrapper = mountForm({ reason: 'a'.repeat(1001) });

    expect(wrapper.get('.char-count').classes()).toContain('over');
    expect(wrapper.get('.char-count').text()).toBe('1001 / 1000 字节');
    expect(wrapper.get('[role="alert"]').text()).toBe('变更原因最多 1000 字节（当前 1001 字节）');
  });

  it('counts 400 CJK characters as 1200 bytes and rejects them', () => {
    const wrapper = mountForm({ reason: '中'.repeat(400) });

    expect(wrapper.get('.char-count').classes()).toContain('over');
    expect(wrapper.get('.char-count').text()).toBe('1200 / 1000 字节');
    expect(wrapper.get('[role="alert"]').text()).toBe('变更原因最多 1000 字节（当前 1200 字节）');
  });

  it('accepts 300 CJK characters (900 bytes)', () => {
    const wrapper = mountForm({ reason: '中'.repeat(300) });

    expect(wrapper.find('[role="alert"]').exists()).toBe(false);
    expect(wrapper.get('.char-count').text()).toBe('900 / 1000 字节');
    expect(wrapper.get('textarea').attributes('aria-invalid')).toBeUndefined();
  });

  it('emits the typed value so the store owns the canonical reason', async () => {
    const wrapper = mountForm();
    await wrapper.get('textarea').setValue('  事故 ID 123  ');
    expect(wrapper.emitted('update:reason')?.at(-1)).toEqual(['  事故 ID 123  ']);
  });
});
