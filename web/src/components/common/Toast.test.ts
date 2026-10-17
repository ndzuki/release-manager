import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { nextTick, ref } from 'vue';
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

/*
 * TASK-304: strip block and line comments before matching selectors against an SFC's
 * text. The style block documents the rule it asserts, so without this a commented-out
 * (or merely explanatory) selector satisfies the assertion even when the live rule is
 * dead — the blind spot the review demonstrated. Toast.vue happens to contain no `//`,
 * so the line-comment pass is only a guard for future edits.
 */
function stripComments(source: string): string {
  return source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/[^\n]*/g, '');
}

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

  it('restarts the full interval when the pointer leaves after the countdown started', async () => {
    vi.useFakeTimers();
    const wrapper = mountToast({ autoDismissMs: 1000, testid: 'late-hover' });

    expect(wrapper.find('[data-testid="late-hover"]').exists()).toBe(true);

    // Let most of the interval elapse BEFORE the pointer arrives: a resume implementation would
    // then have only ~100ms left, while restart owes the reader the whole interval again.
    vi.advanceTimersByTime(900);
    await nextTick();
    expect(wrapper.find('[data-testid="late-hover"]').exists()).toBe(true);

    await wrapper.trigger('mouseenter');
    vi.advanceTimersByTime(5000);
    await nextTick();
    expect(wrapper.find('[data-testid="late-hover"]').exists()).toBe(true);

    await wrapper.trigger('mouseleave');
    await nextTick();
    vi.advanceTimersByTime(999);
    await nextTick();
    expect(wrapper.find('[data-testid="late-hover"]').exists()).toBe(true);

    vi.advanceTimersByTime(1);
    await nextTick();
    expect(wrapper.find('[data-testid="late-hover"]').exists()).toBe(false);
  });

  /*
   * TASK-304, contract hole the independent review probed: `autoDismissMs` and the
   * pages' `v-if="store.notice"` are mutually exclusive in practice. The component's
   * own `visible` never returns to true, so a later notice offered to the same instance
   * (the caller's `v-if` stayed truthy) renders nothing. The first case CHARACTERIZES
   * that limitation — it pins today's documented behaviour, not a desired one — and the
   * next two pin the two remedies the docblock in Toast.vue now spells out: remount
   * with `:key`, or clear the caller's notice from `@dismiss`. If the primitive is ever
   * changed to recover by itself, update the Toast.vue docblock and these three cases
   * together.
   */
  it('does not show a later notice under a retained v-if once autoDismissMs hid it', async () => {
    vi.useFakeTimers();
    const notice = ref('第一条');
    const host = mount({
      components: { Toast },
      setup: () => ({ notice }),
      template: '<Toast v-if="notice" :auto-dismiss-ms="1000" testid="hosted">{{ notice }}</Toast>',
    });
    mounted.push(host);

    expect(host.get('[data-testid="hosted"]').text()).toContain('第一条');

    vi.advanceTimersByTime(1000);
    await nextTick();
    expect(host.find('[data-testid="hosted"]').exists()).toBe(false);

    // The caller swaps the notice in, but its `v-if` never went false: the retained
    // instance keeps `visible === false`, so the new notice is invisible.
    notice.value = '第二条';
    await nextTick();
    expect(host.find('[data-testid="hosted"]').exists()).toBe(false);
  });

  it('shows a later notice when the caller remounts the instance with a key', async () => {
    vi.useFakeTimers();
    const notice = ref('第一条');
    const host = mount({
      components: { Toast },
      setup: () => ({ notice }),
      template: '<Toast v-if="notice" :key="notice" :auto-dismiss-ms="1000" testid="keyed">{{ notice }}</Toast>',
    });
    mounted.push(host);

    vi.advanceTimersByTime(1000);
    await nextTick();
    expect(host.find('[data-testid="keyed"]').exists()).toBe(false);

    // The changed key mounts a fresh instance, whose `visible` starts true again.
    notice.value = '第二条';
    await nextTick();
    expect(host.get('[data-testid="keyed"]').text()).toContain('第二条');
  });

  it('shows a later notice when the caller clears its notice on dismiss', async () => {
    vi.useFakeTimers();
    const notice = ref('第一条');
    const host = mount({
      components: { Toast },
      setup: () => ({ notice, clear: () => { notice.value = ''; } }),
      template: '<Toast v-if="notice" :auto-dismiss-ms="1000" testid="cleared" @dismiss="clear">{{ notice }}</Toast>',
    });
    mounted.push(host);

    vi.advanceTimersByTime(1000);
    await nextTick();
    expect(host.find('[data-testid="cleared"]').exists()).toBe(false);

    // Clearing on `@dismiss` drops the `v-if` to false, so the next notice mounts a
    // fresh instance instead of reusing the hidden one.
    notice.value = '第二条';
    await nextTick();
    expect(host.get('[data-testid="cleared"]').text()).toContain('第二条');
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

    // TASK-304: restore explicitly instead of leaning on the config's `restoreMocks`
    // alone, so the stub cannot outlive this case.
    vi.restoreAllMocks();
  });

  /*
   * TASK-304: the reduced-motion case above stubs `window.matchMedia`. This case runs
   * after it and fails if that stub survives into the rest of the file. vite.config.ts
   * already sets `restoreMocks: true`, so the guarantee holds twice over (the explicit
   * `vi.restoreAllMocks()` above and the config); removing either one alone leaves this
   * green, removing both turns it red.
   */
  it('does not leak the matchMedia stub out of the prefers-reduced-motion case', () => {
    expect(vi.isMockFunction(window.matchMedia)).toBe(false);
  });

  /*
   * BOUNDARY OF THIS ASSERTION (TASK-304): it reads the SFC text, because happy-dom
   * does not evaluate scoped CSS. All it checks is that the reduced-motion override is
   * not written with a zero-specificity `:where(...)` selector, which a class-presence test cannot see. Text presence is not proof of a live
   * rule: a same-named literal inside an HTML comment or a JS string still satisfies this
   * assertion, and a rule can be dead for reasons this text match cannot observe. It does NOT
   * prove the computed style, the compiled specificity, what the build pipeline
   * rewrites, or that a real browser applies the rule; that would need a browser-level
   * check. Comments are stripped first (see `stripComments`): a selector merely
   * mentioned in a comment must not be able to satisfy the assertion. The review's
   * counterexample is exactly that — the live rule replaced by a `:where(...)` variant
   * while the old compound selector survives in a nearby comment.
   *
   * Scope note: the base `animation` lives on `.toast` (0,2,0 once scoped), so a
   * `:where(...)` variant compiles to (0,0,0) and loses: a dead rule a class-presence
   * test cannot see. That number comes from the review's compiled-CSS measurement, not
   * from here, and this assertion does not re-measure it.
   */
  it('disables the entry animation with a rule that outranks the base animation', () => {
    const source = stripComments(readFileSync(resolve(__dirname, 'Toast.vue'), 'utf8'));

    const matches = source.match(/\.toast\.toast--reduced-motion\s*\{[^}]*animation:\s*none/g) ?? [];
    expect(matches).toHaveLength(1);
  });

  // TASK-304: mini-repro of the review's counterexample, pinned so `stripComments`
  // cannot silently regress into "a selector in a comment counts as the rule".
  it('does not count a selector that only appears in a comment', () => {
    const commented =
      '/* .toast.toast--reduced-motion { animation: none } */\n' +
      ':where(.toast--reduced-motion) { animation: none; }\n' +
      '// .toast.toast--reduced-motion { animation: none }\n';

    const rule = /\.toast\.toast--reduced-motion\s*\{[^}]*animation:\s*none/;
    // Without stripping the raw text would pass on the comment alone…
    expect(commented).toMatch(rule);
    // …and stripping is what removes that false positive.
    expect(stripComments(commented)).not.toMatch(rule);
  });

  /*
   * The host template fixes the order, so the ordering half is a regression guard only —
   * any implementation that renders its slot passes it. TASK-304 adds the half with
   * teeth: one success and one error sibling must derive their own tone class and their
   * own live-region role, so an implementation that drops or fixes the per-instance
   * binding renders two indistinguishable notices.
   */
  it('keeps sibling notices ordered and distinguishable', () => {
    const host = mount({
      components: { Toast },
      template:
        '<div class="toast-host"><Toast testid="first" tone="success">第一条</Toast><Toast testid="second" tone="error">第二条</Toast></div>',
    });
    mounted.push(host);

    const notices = Array.from((host.element as HTMLElement).querySelectorAll<HTMLElement>('.toast-host > .toast'));
    expect(notices).toHaveLength(2);
    expect(notices.map((notice) => notice.dataset.testid)).toEqual(['first', 'second']);
    expect(notices[0]!.textContent).toContain('第一条');
    expect(notices[1]!.textContent).toContain('第二条');

    expect(notices[0]!.classList.contains('toast--success')).toBe(true);
    expect(notices[0]!.classList.contains('toast--error')).toBe(false);
    expect(notices[1]!.classList.contains('toast--error')).toBe(true);
    expect(notices[1]!.classList.contains('toast--success')).toBe(false);
    expect(notices[0]!.getAttribute('role')).toBe('status');
    expect(notices[1]!.getAttribute('role')).toBe('alert');
  });
});
