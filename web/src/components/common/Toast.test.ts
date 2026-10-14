import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { nextTick } from 'vue';
import Toast from './Toast.vue';

/*
 * TASK-299. The primitive's contract, pinned down one clause at a time. The negative
 * assertions (does not disappear, does not move focus) are always backed by a positive
 * one first — otherwise a toast that never rendered would satisfy them.
 */
const mounted: Array<{ unmount: () => void }> = [];

function mountToast(props: Record<string, unknown> = {}, slot = '已保存') {
  const wrapper = mount(Toast, {
    attachTo: document.body,
    props,
    slots: { default: () => slot },
  });
  mounted.push(wrapper);
  return wrapper;
}

afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
  vi.useRealTimers();
});

describe('Toast', () => {
  it('maps the tone to the live-region role, never downgrading an error', () => {
    const info = mountToast();
    expect(info.attributes('role')).toBe('status');
    expect(info.classes()).toContain('toast--info');

    const success = mountToast({ tone: 'success' });
    expect(success.attributes('role')).toBe('status');
    expect(success.classes()).toContain('toast--success');

    // Assertive: an error announced politely can be missed while the user types.
    const error = mountToast({ tone: 'error' });
    expect(error.attributes('role')).toBe('alert');
    expect(error.classes()).toContain('toast--error');
  });

  it('renders the caller text and keeps the caller testid', () => {
    const wrapper = mountToast({ testid: 'definitions-notice' }, '已更新定义 demo（版本 4）');

    const notice = wrapper.get('[data-testid="definitions-notice"]');
    expect(notice.text()).toContain('已更新定义 demo（版本 4）');
  });

  /*
   * The corrected premise of this task: every existing notice in the console is
   * persistent, so an absent `autoDismissMs` must mean "never", not "some default".
   * The mutation that gives the prop a default is what this test has to catch.
   */
  it('stays on screen forever when autoDismissMs is omitted', async () => {
    vi.useFakeTimers();
    const wrapper = mountToast({ testid: 'persistent' });

    expect(wrapper.find('[data-testid="persistent"]').exists()).toBe(true);

    vi.advanceTimersByTime(6 * 60 * 60 * 1000);
    await nextTick();

    expect(wrapper.find('[data-testid="persistent"]').exists()).toBe(true);
  });

  it('dismisses after the explicitly requested delay and emits dismiss', async () => {
    vi.useFakeTimers();
    const wrapper = mountToast({ autoDismissMs: 1000 });

    expect(wrapper.find('p').exists()).toBe(true);

    vi.advanceTimersByTime(999);
    await nextTick();
    expect(wrapper.find('p').exists()).toBe(true);

    vi.advanceTimersByTime(1);
    await nextTick();
    expect(wrapper.find('p').exists()).toBe(false);
    expect(wrapper.emitted('dismiss')).toHaveLength(1);
  });

  it('pauses the countdown while the pointer is over it', async () => {
    vi.useFakeTimers();
    const wrapper = mountToast({ autoDismissMs: 1000, testid: 'hovered' });

    expect(wrapper.find('[data-testid="hovered"]').exists()).toBe(true);

    await wrapper.trigger('mouseenter');
    vi.advanceTimersByTime(5000);
    await nextTick();
    expect(wrapper.find('[data-testid="hovered"]').exists()).toBe(true);

    // Leaving restarts the full interval rather than resuming a remainder.
    await wrapper.trigger('mouseleave');
    vi.advanceTimersByTime(999);
    await nextTick();
    expect(wrapper.find('[data-testid="hovered"]').exists()).toBe(true);

    vi.advanceTimersByTime(1);
    await nextTick();
    expect(wrapper.find('[data-testid="hovered"]').exists()).toBe(false);
  });

  it('pauses while focus is inside and never moves focus when it disappears', async () => {
    vi.useFakeTimers();
    const outside = document.createElement('button');
    outside.textContent = 'outside';
    document.body.append(outside);
    outside.focus();

    const wrapper = mountToast({ autoDismissMs: 1000, testid: 'focused' });
    const dismiss = wrapper.get('[data-testid="focused-dismiss"]');
    (dismiss.element as HTMLElement).focus();
    await wrapper.trigger('focusin');

    // Positive backing: focus really is inside the toast.
    expect(document.activeElement).toBe(dismiss.element);

    vi.advanceTimersByTime(5000);
    await nextTick();
    expect(wrapper.find('[data-testid="focused"]').exists()).toBe(true);

    outside.focus();
    await wrapper.trigger('focusout');
    vi.advanceTimersByTime(1000);
    await nextTick();

    expect(wrapper.find('[data-testid="focused"]').exists()).toBe(false);
    // The removal itself must not touch the focus the user had before.
    expect(document.activeElement).toBe(outside);
  });

  it('does not steal focus when it appears', async () => {
    vi.useFakeTimers();
    const typing = document.createElement('input');
    document.body.append(typing);
    typing.focus();
    expect(document.activeElement).toBe(typing);

    const wrapper = mountToast({ autoDismissMs: 500, testid: 'appearing' });
    await nextTick();

    expect(wrapper.find('[data-testid="appearing"]').exists()).toBe(true);
    expect(document.activeElement).toBe(typing);

    vi.advanceTimersByTime(500);
    await nextTick();
    expect(wrapper.find('[data-testid="appearing"]').exists()).toBe(false);
    expect(document.activeElement).toBe(typing);
  });

  it('gives the dismiss control an accessible name', () => {
    const wrapper = mountToast({ testid: 'named' });
    const dismiss = wrapper.get('[data-testid="named-dismiss"]');

    expect(dismiss.element.tagName).toBe('BUTTON');
    // The glyph is decorative, so the name has to come from aria-label.
    expect(dismiss.attributes('aria-label')).toBe('关闭通知');
    expect(dismiss.text()).toBe('×');
  });

  it('dismisses on the close button and emits dismiss', async () => {
    const wrapper = mountToast({ testid: 'closable' });

    expect(wrapper.find('[data-testid="closable"]').exists()).toBe(true);
    await wrapper.get('[data-testid="closable-dismiss"]').trigger('click');

    expect(wrapper.find('[data-testid="closable"]').exists()).toBe(false);
    expect(wrapper.emitted('dismiss')).toHaveLength(1);
  });

  it('honours prefers-reduced-motion', () => {
    const matchMedia = vi.spyOn(window, 'matchMedia');
    matchMedia.mockReturnValue({ matches: true } as MediaQueryList);

    const still = mountToast();
    expect(still.classes()).toContain('toast--reduced-motion');
    expect(matchMedia).toHaveBeenCalledWith('(prefers-reduced-motion: reduce)');

    matchMedia.mockReturnValue({ matches: false } as MediaQueryList);
    const animated = mountToast();
    expect(animated.classes()).not.toContain('toast--reduced-motion');
  });

  /*
   * The class above only proves the preference was read. happy-dom does not evaluate
   * scoped CSS, so the rule that actually stops the animation is asserted on the SFC
   * text — the same split a11y-baseline.test.ts documents. The compound selector is the
   * point: the base `animation` lives on `.toast` (0,2,0 once scoped), so a
   * `:where(...)` variant would score (0,1,0) and leave the animation running: a dead
   * rule that a class-presence test cannot see.
   */
  it('disables the entry animation with a rule that outranks the base animation', () => {
    const source = readFileSync(resolve(__dirname, 'Toast.vue'), 'utf8');

    expect(source).toMatch(/\.toast\.toast--reduced-motion\s*\{[^}]*animation:\s*none/);
  });

  it('keeps sibling notices ordered and distinguishable', () => {
    const host = mount({
      components: { Toast },
      template:
        '<div class="toast-host"><Toast testid="first">第一条</Toast><Toast testid="second">第二条</Toast></div>',
    });
    mounted.push(host);

    const notices = Array.from((host.element as HTMLElement).querySelectorAll<HTMLElement>('.toast-host > .toast'));
    expect(notices).toHaveLength(2);
    expect(notices.map((notice) => notice.dataset.testid)).toEqual(['first', 'second']);
    expect(notices[0]!.textContent).toContain('第一条');
    expect(notices[1]!.textContent).toContain('第二条');
  });
});
