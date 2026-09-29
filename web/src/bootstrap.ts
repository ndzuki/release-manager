import { t } from '@/i18n/messages';
/*
 * Boot-failure surface (B2 / docs/ux-review.md).
 *
 * `main.ts` awaits `auth.initialize()` BEFORE `app.mount('#app')`. When the API
 * was unreachable that promise rejected, nothing ever mounted, and the user saw a
 * fully blank page: no message, no retry, and nothing in the UI to distinguish
 * "backend down" from "console broken". Browser-measured baseline for that state
 * is recorded in docs/ux-review.md (B2): `#app` innerHTML length 0, DOM node
 * count 1, screenshot `docs/images/user-manual/22-blank-page-backend-down.png`.
 *
 * This module renders the surface that failure never had. It is deliberately
 * framework-free: it must work when the Vue app (or its store bootstrap) is
 * exactly what failed. Built with DOM APIs rather than `v-html` (ADR-029
 * clause 6), so an error message can never be interpreted as markup.
 */

/**
 * Hard ceiling on bootstrap (AC-178-06).
 *
 * `renderBootFailure` is only reached when the bootstrap promise REJECTS. A
 * blackholed network never rejects — the awaited RPC simply never settles — so
 * the page stayed blank forever even with the failure surface in place. This
 * deadline converts "never settles" into a rejection.
 *
 * It does not cancel the work underneath: if initialize() later succeeds, the
 * store is initialised while the failure page is shown, and the user's only way
 * forward is a reload. Bounded and visible beats correct-and-blank.
 */
export const BOOT_TIMEOUT_MS = 20_000;

export class BootTimeoutError extends Error {
  constructor(timeoutMs: number) {
    super(t('bootstrap.timeout', { timeoutMs }));
    this.name = 'BootTimeoutError';
  }
}

/** Rejects after `timeoutMs` even if `promise` never settles. */
export function withTimeout<T>(promise: Promise<T>, timeoutMs: number = BOOT_TIMEOUT_MS): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new BootTimeoutError(timeoutMs)), timeoutMs);
    // Both outcomes are handled, so a late rejection cannot surface as an
    // unhandled rejection after we already gave up.
    promise.then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      (error: unknown) => {
        clearTimeout(timer);
        reject(error);
      },
    );
  });
}

export interface BootFailureOptions {
  title?: string;
  /** Invoked by the retry button; defaults to a full page reload. */
  onRetry?: () => void;
}

function describe(error: unknown): string {
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === 'string' && error) return error;
  return t('bootstrap.unknownError');
}

/**
 * Renders the boot-failure page into `root`, replacing its content.
 *
 * @returns the retry button, so callers (and tests) can drive it.
 */
export function renderBootFailure(
  root: Element,
  error: unknown,
  options: BootFailureOptions = {},
): HTMLButtonElement {
  const title = options.title ?? t('bootstrap.unreachable');
  const retry = options.onRetry ?? (() => globalThis.location.reload());

  root.textContent = '';

  const main = document.createElement('main');
  main.className = 'boot-failure';
  main.setAttribute('role', 'alert');
  main.dataset.testid = 'boot-failure';

  const heading = document.createElement('h1');
  heading.className = 'boot-failure__title';
  heading.textContent = title;

  const message = document.createElement('p');
  message.className = 'boot-failure__message';
  message.textContent = t('bootstrap.failed');

  const detail = document.createElement('pre');
  detail.className = 'boot-failure__detail';
  // textContent, never innerHTML: the reason comes from the network layer.
  detail.textContent = describe(error);

  const retryButton = document.createElement('button');
  retryButton.type = 'button';
  retryButton.className = 'boot-failure__retry';
  retryButton.textContent = t('action.retry');
  retryButton.addEventListener('click', () => retry());

  main.append(heading, message, detail, retryButton);
  root.append(main);
  retryButton.focus();
  return retryButton;
}
