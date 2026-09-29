import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { BOOT_TIMEOUT_MS, BootTimeoutError, renderBootFailure, withTimeout } from './bootstrap';

// B2 regression: a rejected bootstrap used to render NOTHING — the app never
// mounted, so the user got a blank white page with no message and no retry
// (verified in a real browser: #app innerHTML length 0, DOM node count 1).
describe('renderBootFailure', () => {
  function mount(): HTMLElement {
    const root = document.createElement('div');
    document.body.append(root);
    return root;
  }

  it('renders a titled alert with the reason and a retry button', () => {
    const root = mount();

    const retry = renderBootFailure(root, new Error('HTTP 502'));

    const alert = root.querySelector('[data-testid="boot-failure"]');
    expect(alert).not.toBeNull();
    expect(alert?.getAttribute('role')).toBe('alert');
    expect(root.textContent).toContain('无法连接发布管理服务');
    expect(root.textContent).toContain('HTTP 502');
    // The retry affordance is focusable and focused, so a keyboard user is not
    // stranded on a dead page.
    expect(retry.textContent).toBe('重试');
    expect(document.activeElement).toBe(retry);
  });

  it('never interprets the failure reason as markup', () => {
    const root = mount();

    renderBootFailure(root, new Error('<img src=x onerror="globalThis.pwned = true">'));

    expect(root.querySelector('img')).toBeNull();
    expect(root.textContent).toContain('<img src=x');
    expect((globalThis as { pwned?: boolean }).pwned).toBeUndefined();
  });

  it('calls the retry callback on click exactly once per click', () => {
    const root = mount();
    const onRetry = vi.fn();

    const retry = renderBootFailure(root, new Error('boom'), { onRetry });
    retry.click();
    retry.click();

    expect(onRetry).toHaveBeenCalledTimes(2);
  });

  it('handles non-Error reasons without rendering "undefined"', () => {
    const root = mount();

    renderBootFailure(root, { reason: 'unavailable' });
    renderBootFailure(root, undefined);

    expect(root.textContent).toContain('未知的启动错误。');
  });
});

/*
 * AC-178-06: the failure surface is only reached from a REJECTION, so a bootstrap
 * that never settles (blackholed network) stayed blank forever.
 */
describe('withTimeout', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('passes through a bootstrap that settles in time', async () => {
    await expect(withTimeout(Promise.resolve('ready'), 1_000)).resolves.toBe('ready');
  });

  it('rejects a bootstrap that never settles', async () => {
    vi.useFakeTimers();
    const never = new Promise<string>(() => {});

    const settled = withTimeout(never, 1_000).then(
      () => 'resolved',
      (error: unknown) => (error instanceof BootTimeoutError ? 'timed-out' : 'other'),
    );

    await vi.advanceTimersByTimeAsync(1_001);

    await expect(settled).resolves.toBe('timed-out');
    expect(BOOT_TIMEOUT_MS).toBeGreaterThan(0);
  });

  it('propagates a real bootstrap failure unchanged', async () => {
    const boom = new Error('api unreachable');
    await expect(withTimeout(Promise.reject(boom), 1_000)).rejects.toBe(boom);
  });
});

/*
 * If the module graph itself fails to evaluate, main.ts never runs: the inline
 * guard in index.html is the only code that can report it. These tests execute
 * that exact script text (not a copy), so the guard is proven to work with no
 * imports, no bundler and no app instance.
 */
describe('index.html inline boot guard', () => {
  // import.meta.url is an http:// URL under the happy-dom runner, so resolve the
  // file against the runner's cwd (web/) instead.
  function readIndexHtml(): string {
    for (const candidate of ['index.html', 'web/index.html']) {
      try {
        return readFileSync(resolve(process.cwd(), candidate), 'utf8');
      } catch {
        // try the next candidate
      }
    }
    throw new Error(`index.html not found from ${process.cwd()}`);
  }
  const html = readIndexHtml();

  function runGuard(): void {
    const start = html.indexOf('<script>');
    const end = html.indexOf('</script>', start);
    const script = html.slice(start + '<script>'.length, end);
    // The point is to run the real inline script text.
    new Function(script)();
  }

  it('sits before the app module and has no imports or bundler dependencies', () => {
    const guardIndex = html.indexOf("addEventListener('error'");
    const moduleIndex = html.indexOf('src="/src/main.ts"');
    expect(guardIndex).toBeGreaterThan(-1);
    expect(guardIndex).toBeLessThan(moduleIndex);

    const script = html.slice(html.indexOf('<script>'), html.indexOf('</script>'));
    expect(script).not.toContain('import ');
    expect(script).not.toContain('require(');
  });

  it('renders a reloadable surface when a module-level error fires', () => {
    document.body.innerHTML = '<div id="app"></div>';
    (globalThis as { __rmBootMounted?: boolean }).__rmBootMounted = false;
    runGuard();

    window.dispatchEvent(new ErrorEvent('error', { message: 'Failed to fetch dynamically imported module' }));

    const app = document.getElementById('app')!;
    expect(app.textContent).toContain('无法启动发布管理控制台');
    expect(app.textContent).toContain('Failed to fetch dynamically imported module');
    expect(app.querySelector('.boot-failure__retry')?.textContent).toBe('重新加载');
    expect(app.querySelector('[role="alert"]')).not.toBeNull();
  });

  it('catches a non-bubbling resource-load error (the common blank-SPA cause)', () => {
    document.body.innerHTML = '<div id="app"></div>';
    (globalThis as { __rmBootMounted?: boolean }).__rmBootMounted = false;
    runGuard();

    // A missing entry bundle / lazy chunk dispatches a PLAIN `error` Event (no
    // message) on the ELEMENT, and it does not bubble — so only a capture-phase
    // listener on window sees it, and the asset has to be named from target.src.
    const failedScript = document.createElement('script');
    failedScript.src = '/assets/index-deadbeef.js';
    document.body.append(failedScript);
    const event = new Event('error');
    expect(event.bubbles).toBe(false);
    expect((event as Event & { message?: string }).message).toBeUndefined();
    failedScript.dispatchEvent(event);

    const app = document.getElementById('app')!;
    expect(app.textContent).toContain('无法启动发布管理控制台');
    expect(app.textContent).toContain('index-deadbeef.js');
  });

  it('also catches an unhandled rejection and stays quiet after a successful mount', () => {
    document.body.innerHTML = '<div id="app"></div>';
    (globalThis as { __rmBootMounted?: boolean }).__rmBootMounted = false;
    runGuard();

    // happy-dom has no PromiseRejectionEvent; the inline handler only reads
    // `event.reason`, so a plain event carrying it exercises the same path.
    const rejection = new Event('unhandledrejection') as Event & { reason?: unknown };
    rejection.reason = new Error('chunk load failed');
    window.dispatchEvent(rejection);
    expect(document.getElementById('app')!.textContent).toContain('chunk load failed');

    // Once the console is mounted, a late unrelated error must not replace it.
    document.body.innerHTML = '<div id="app"><p>console</p></div>';
    (globalThis as { __rmBootMounted?: boolean }).__rmBootMounted = true;
    window.dispatchEvent(new ErrorEvent('error', { message: 'late failure' }));
    expect(document.getElementById('app')!.textContent).toBe('console');
  });
});
