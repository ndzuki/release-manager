import { computed, readonly, ref, watch } from 'vue';

/*
 * Theme preference for the console (direction C).
 *
 * Three states, not two: `system` follows the OS, `light` and `dark` override it. The
 * choice is persisted in localStorage under `rm-theme` and applied as
 * `document.documentElement.dataset.theme`, which is what tokens.css keys its explicit
 * light/dark palette on. The stylesheet ALSO carries a `prefers-color-scheme` fallback so
 * the first paint is already correct before this module runs; `data-theme` then makes the
 * resolution explicit and keeps it right if the OS changes.
 *
 * Two non-obvious decisions:
 *  - the state is module-level, not per-call, because the theme is a property of the
 *    document: two components calling useTheme() must not fight over different values;
 *  - every browser access is guarded by `browserReady()`, so importing this module in a
 *    non-DOM context (SSR, a unit test without happy-dom) is inert instead of throwing on
 *    `window`/`document`.
 */

export type ThemePreference = 'system' | 'light' | 'dark';
export type ResolvedTheme = 'light' | 'dark';

/** Storage key for the preference; exported so a test does not restate the literal. */
export const THEME_STORAGE_KEY = 'rm-theme';
/** Query whose `matches` decides the resolution while the preference is `system`. */
export const DARK_MEDIA_QUERY = '(prefers-color-scheme: dark)';

/** `toggle()` cycles through all three states; the order is the cycle. */
const CYCLE: readonly ThemePreference[] = ['system', 'light', 'dark'];

const preference = ref<ThemePreference>('system');
const systemPrefersDark = ref(false);

let started = false;

function browserReady(): boolean {
  return typeof window !== 'undefined' && typeof document !== 'undefined';
}

function normalize(value: unknown): ThemePreference {
  return value === 'light' || value === 'dark' ? value : 'system';
}

function readStoredPreference(): ThemePreference {
  if (!browserReady()) return 'system';
  try {
    // A disabled or unavailable store (private mode, quota, policy) must not break the
    // console: fall back to `system` and keep the in-memory value authoritative.
    return normalize(window.localStorage.getItem(THEME_STORAGE_KEY));
  } catch {
    return 'system';
  }
}

function persistPreference(theme: ThemePreference): void {
  if (!browserReady()) return;
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, theme);
  } catch {
    // Storage is a convenience, not the source of truth for this session.
  }
}

export const resolvedTheme = computed<ResolvedTheme>(() =>
  preference.value === 'system' ? (systemPrefersDark.value ? 'dark' : 'light') : preference.value,
);

function applyToDocument(): void {
  if (!browserReady()) return;
  document.documentElement.dataset.theme = resolvedTheme.value;
}

function handleSystemChange(event: MediaQueryListEvent): void {
  systemPrefersDark.value = event.matches;
  // Apply synchronously: the watcher below would also do it, but the DOM should already
  // be correct when the listener returns (and a test does not have to flush).
  applyToDocument();
}

/** Reads storage, subscribes to the OS preference and applies the theme once. */
function start(): void {
  if (started || !browserReady()) return;
  started = true;
  preference.value = readStoredPreference();
  const query = window.matchMedia(DARK_MEDIA_QUERY);
  systemPrefersDark.value = query.matches;
  // The listener lives for the document's lifetime, not a component's: the preference is
  // global, and the shell is mounted once for the whole authenticated session.
  query.addEventListener('change', handleSystemChange);
  watch(resolvedTheme, applyToDocument, { immediate: true });
}

export function setTheme(next: ThemePreference): void {
  preference.value = normalize(next);
  persistPreference(preference.value);
  // The watcher above would do this on the next flush; applying it here as well keeps
  // `setTheme()` synchronous for callers that read the DOM immediately.
  applyToDocument();
}

export function toggle(): void {
  const index = CYCLE.indexOf(preference.value);
  setTheme(CYCLE[(index + 1) % CYCLE.length]!);
}

export function useTheme() {
  start();
  return { theme: readonly(preference), resolvedTheme, setTheme, toggle };
}

export type UseTheme = ReturnType<typeof useTheme>;
