import { flushPromises, mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vitest';
import CopyableHash from './CopyableHash.vue';

/*
 * CopyableHash contract (TASK-394).
 *
 * The value shown is truncated but the value COPIED is complete, so each case asserts
 * both: a component that copies its own rendered slice would look correct in a snapshot
 * and silently corrupt every pasted digest. The failure path is asserted on VISIBLE text
 * because happy-dom has no clipboard permission model — the DOM is the only thing a test
 * can observe, and it is also what a user observes.
 */
const FULL = 'sha256:0123456789abcdef0123456789abcdef';

function mountHash(props: Record<string, unknown> = {}) {
  return mount(CopyableHash, { props: { value: FULL, ...props } });
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('CopyableHash', () => {
  it('shows the head with an ellipsis and keeps the full value in title', () => {
    const wrapper = mountHash();
    const code = wrapper.get('code');

    expect(code.text()).toBe('sha256:0…');
    expect(code.attributes('title')).toBe(FULL);
    expect(code.classes()).toContain('mono');
  });

  it('keeps a tail when one is requested', () => {
    const wrapper = mountHash({ head: 7, tail: 4 });

    expect(wrapper.get('code').text()).toBe('sha256:…cdef');
  });

  it('renders short values whole instead of truncating them', () => {
    const wrapper = mountHash({ value: 'op-1' });

    expect(wrapper.get('code').text()).toBe('op-1');
    expect(wrapper.get('code').attributes('title')).toBe('op-1');
  });

  it('copies the full value, not the rendered slice', async () => {
    const writeText = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined);
    const wrapper = mountHash();

    await wrapper.get('button').trigger('click');
    await flushPromises();

    expect(writeText).toHaveBeenCalledWith(FULL);
    expect(wrapper.get('.copyable-hash__feedback').text()).toBe('已复制');
    expect(wrapper.get('.copyable-hash__feedback').attributes('role')).toBe('status');
  });

  it('gives the copy control an accessible name that says what is copied', () => {
    const wrapper = mountHash();

    expect(wrapper.get('button').attributes('aria-label')).toBe('复制完整值');
    expect(wrapper.get('button').text()).toBe('复制');
  });

  it('reports a refused clipboard write visibly', async () => {
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValueOnce(new Error('denied'));
    const wrapper = mountHash();

    await wrapper.get('button').trigger('click');
    await flushPromises();

    const feedback = wrapper.get('.copyable-hash__feedback');
    expect(feedback.text()).toBe('复制失败，请手动选中复制');
    expect(feedback.classes()).toContain('copyable-hash__feedback--failed');
  });

  it('reports a missing clipboard API (insecure origin) as a failure', async () => {
    const descriptor = Object.getOwnPropertyDescriptor(navigator, 'clipboard');
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
    try {
      const wrapper = mountHash();

      await wrapper.get('button').trigger('click');
      await flushPromises();

      expect(wrapper.get('.copyable-hash__feedback').text()).toBe('复制失败，请手动选中复制');
    } finally {
      if (descriptor) Object.defineProperty(navigator, 'clipboard', descriptor);
      else Reflect.deleteProperty(navigator, 'clipboard');
    }
  });
});
