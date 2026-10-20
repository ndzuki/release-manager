import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest';

/*
 * useTheme contract (TASK-394).
 *
 * The module owns document-wide state, so every case re-imports a fresh copy
 * (`vi.resetModules`) instead of fighting the singleton. Four properties are pinned:
 * `system` is the default, an explicit choice persists under `rm-theme`, `toggle()`
 * walks the three-state cycle, and while the preference IS `system` an OS change moves
 * the rendered theme — which is the whole reason `system` exists. A fifth case covers the
 * non-DOM import: the module must be inert, not throw, when there is no `window`.
 */
const DARK_MEDIA_QUERY = '(prefers-color-scheme: dark)';

function installMatchMedia(initialMatches: boolean) {
  const listeners = new Set<(event: MediaQueryListEvent) => void>();
  const query = {
    matches: initialMatches,
    media: DARK_MEDIA_QUERY,
    addEventListener: (_type: string, listener: (event: MediaQueryListEvent) => void) => {
      listeners.add(listener);
    },
    removeEventListener: (_type: string, listener: (event: MediaQueryListEvent) => void) => {
      listeners.delete(listener);
    },
  };
  vi.spyOn(window, 'matchMedia').mockReturnValue(query as unknown as MediaQueryList);
  return {
    /** Simulates the OS switching its preference. */
    emit(matches: boolean) {
      query.matches = matches;
      for (const listener of listeners) listener({ matches } as MediaQueryListEvent);
    },
    listenerCount: () => listeners.size,
  };
}

async function loadThemeModule() {
  vi.resetModules();
  return import('./useTheme');
}

/*
 * happy-dom as configured here exposes no `localStorage`, while every real browser does.
 * The module already degrades safely when storage throws or is missing; the tests need a
 * working store to assert the persistence contract, so they install a minimal one and
 * restore the environment afterwards.
 */
const originalLocalStorage = Object.getOwnPropertyDescriptor(window, 'localStorage');

function installLocalStorage(): Map<string, string> {
  const store = new Map<string, string>();
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    value: {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => {
        store.set(key, String(value));
      },
      removeItem: (key: string) => {
        store.delete(key);
      },
      clear: () => store.clear(),
      key: (index: number) => [...store.keys()][index] ?? null,
      get length() {
        return store.size;
      },
    },
  });
  return store;
}

let store: Map<string, string>;

beforeEach(() => {
  store = installLocalStorage();
  delete document.documentElement.dataset.theme;
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  delete document.documentElement.dataset.theme;
  if (originalLocalStorage) Object.defineProperty(window, 'localStorage', originalLocalStorage);
  else Reflect.deleteProperty(window, 'localStorage');
  store.clear();
});

describe('useTheme', () => {
  it('defaults to system and resolves it through the OS preference', async () => {
    installMatchMedia(true);
    const { useTheme } = await loadThemeModule();

    const theme = useTheme();

    expect(theme.theme.value).toBe('system');
    expect(theme.resolvedTheme.value).toBe('dark');
    expect(document.documentElement.dataset.theme).toBe('dark');
  });

  it('resolves system to light when the OS prefers light', async () => {
    installMatchMedia(false);
    const { useTheme } = await loadThemeModule();

    const theme = useTheme();

    expect(theme.theme.value).toBe('system');
    expect(theme.resolvedTheme.value).toBe('light');
    expect(document.documentElement.dataset.theme).toBe('light');
  });

  it('restores a stored preference before falling back to the OS', async () => {
    installMatchMedia(true);
    window.localStorage.setItem('rm-theme', 'light');
    const { useTheme } = await loadThemeModule();

    const theme = useTheme();

    expect(theme.theme.value).toBe('light');
    expect(theme.resolvedTheme.value).toBe('light');
    expect(document.documentElement.dataset.theme).toBe('light');
  });

  it('persists an explicit choice under rm-theme', async () => {
    installMatchMedia(false);
    const { useTheme } = await loadThemeModule();
    const theme = useTheme();

    theme.setTheme('dark');
    expect(window.localStorage.getItem('rm-theme')).toBe('dark');
    expect(document.documentElement.dataset.theme).toBe('dark');

    theme.setTheme('light');
    expect(window.localStorage.getItem('rm-theme')).toBe('light');
    expect(document.documentElement.dataset.theme).toBe('light');
  });

  it('toggles through the three-state cycle', async () => {
    installMatchMedia(true);
    const { useTheme } = await loadThemeModule();
    const theme = useTheme();

    expect(theme.theme.value).toBe('system');
    theme.toggle();
    expect(theme.theme.value).toBe('light');
    theme.toggle();
    expect(theme.theme.value).toBe('dark');
    theme.toggle();
    expect(theme.theme.value).toBe('system');
    expect(window.localStorage.getItem('rm-theme')).toBe('system');
  });

  it('follows an OS change while system is selected', async () => {
    const media = installMatchMedia(false);
    const { useTheme } = await loadThemeModule();
    const theme = useTheme();
    expect(theme.resolvedTheme.value).toBe('light');

    media.emit(true);

    expect(theme.resolvedTheme.value).toBe('dark');
    expect(document.documentElement.dataset.theme).toBe('dark');
    // Nothing was persisted: the OS is still the source of truth.
    expect(window.localStorage.getItem('rm-theme')).toBeNull();
  });

  it('stops following the OS once a theme is chosen explicitly', async () => {
    const media = installMatchMedia(false);
    const { useTheme } = await loadThemeModule();
    const theme = useTheme();

    theme.setTheme('dark');
    media.emit(false);

    expect(theme.resolvedTheme.value).toBe('dark');
    expect(document.documentElement.dataset.theme).toBe('dark');
  });

  it('is inert when there is no DOM (SSR/test import safety)', async () => {
    vi.stubGlobal('window', undefined);
    const { useTheme } = await loadThemeModule();

    const theme = useTheme();
    expect(() => theme.setTheme('dark')).not.toThrow();
    expect(theme.theme.value).toBe('dark');
    expect(theme.resolvedTheme.value).toBe('dark');
  });
});
