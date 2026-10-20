import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

/*
 * Token guards (TASK-180).
 *
 * The colour/font migration was driven by scripts that proved each literal equal
 * to its token value before substituting. The scripts were throwaway, so this
 * test pins the invariants and the equivalence pairs instead:
 *
 *  - the console must not reference an undefined token (the original defect:
 *    46 `var(--x, fallback)` references and ZERO definitions);
 *  - it must not define a token nobody uses (a second vocabulary for the same
 *    values is how `--font-size-sm` silently absorbed 0.85rem/0.8rem during the
 *    dialog rewrite — the independent review caught it as a blocker);
 *  - the pinned pairs below record the exact literal each token replaced, so a
 *    later "semantic tidy-up" that changes a value fails here instead of shipping.
 */
const webRoot = (() => {
  for (const candidate of ['.', 'web']) {
    try {
      statSync(resolve(process.cwd(), candidate, 'src/styles/tokens.css'));
      return candidate;
    } catch {
      // next candidate
    }
  }
  throw new Error(`tokens.css not found from ${process.cwd()}`);
})();

const tokensCss = readFileSync(resolve(webRoot, 'src/styles/tokens.css'), 'utf8');
const declared = new Map<string, string>();
for (const match of tokensCss.matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/g)) {
  declared.set(match[1]!, match[2]!.trim());
}

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) {
      if (entry === 'gen' || entry === 'node_modules') continue;
      sourceFiles(path, out);
    } else if (/\.(vue|ts|css)$/.test(entry) && !entry.endsWith('.test.ts')) {
      out.push(path);
    }
  }
  return out;
}

const files = sourceFiles(resolve(webRoot, 'src'));
const referenced = new Set<string>();
for (const file of files) {
  const text = readFileSync(file, 'utf8');
  for (const match of text.matchAll(/var\((--[a-z0-9-]+)[,)]/g)) referenced.add(match[1]!);
}

describe('design tokens', () => {
  it('defines every token the console references', () => {
    const missing = [...referenced].filter((token) => !declared.has(token)).sort();
    expect(missing).toEqual([]);
  });

  it('has no token without a consumer', () => {
    const unused = [...declared.keys()].filter((token) => !referenced.has(token)).sort();
    expect(unused).toEqual([]);
  });

  /*
   * TASK-394 replaced the migration's byte-identical pins with an ALIAS contract.
   *
   * The old pins recorded "this token replaced exactly this literal"; direction C
   * re-assigns the values on purpose, so pinning the literals would freeze the console to
   * one theme. What must stay true instead is that every legacy name resolves THROUGH the
   * short palette (directly or via color-mix): that is the mechanism which makes one
   * declaration follow both themes. A raw hex reappearing in a --color-* declaration is
   * the regression this catches — it would pin that surface to one theme and produce
   * white-on-white (or black-on-black) in the other.
   */
  const ALIASES: Record<string, string> = {
    '--color-bg': 'var(--bg)',
    '--color-surface': 'var(--sf)',
    '--color-surface-muted': 'var(--sf2)',
    '--color-border': 'var(--bd)',
    '--color-text': 'var(--tx)',
    '--color-muted': 'var(--mu)',
    '--color-primary': 'var(--pr)',
    '--color-primary-hover': 'color-mix(in srgb, var(--pr) 85%, var(--bg))',
    '--color-on-accent': 'var(--on)',
    '--color-error': 'var(--er)',
    '--color-danger': 'var(--er)',
    '--color-warning': 'var(--wn)',
    '--color-success': 'var(--ok)',
    '--color-info': 'var(--k)',
    '--color-cyan': 'var(--k)',
    '--color-violet': 'var(--n)',
  };

  it.each(Object.entries(ALIASES))('resolves %s through the palette', (token, expected) => {
    expect(declared.get(token)).toBe(expected);
  });

  it('expresses every --color-* alias through the palette instead of a raw hex', () => {
    const frozen = [...declared.entries()]
      .filter(([name]) => name.startsWith('--color-'))
      .filter(([, value]) => /#[0-9a-f]{3,8}\b/i.test(value))
      .map(([name, value]) => `${name}: ${value}`);

    expect(frozen).toEqual([]);
  });

  /** Declarations inside one `{ … }` block, so the two explicit palettes can be pinned. */
  function declaredIn(selector: string): Map<string, string> {
    // The selector contains regex metacharacters (`:not(…)`, `[data-theme='dark']`), so
    // it is escaped rather than used as a pattern.
    const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    const block = new RegExp(`${escaped}\\s*\\{([\\s\\S]*?)\\}`).exec(tokensCss)?.[1] ?? '';
    const map = new Map<string, string>();
    for (const match of block.matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/g)) map.set(match[1]!, match[2]!.trim());
    return map;
  }

  const darkPalette = declaredIn("[data-theme='dark']");
  const lightPalette = declaredIn("[data-theme='light']");

  /*
   * The direction-C sample was approved byte for byte ("务必逐字节照抄颜色"), so both
   * palettes are pinned exactly. `--r` is theme-invariant and lives once in :root.
   */
  it('pins the dark palette byte for byte', () => {
    expect(Object.fromEntries(darkPalette)).toMatchObject({
      '--bg': '#09090b',
      '--sf': '#111114',
      '--sf2': '#18181b',
      '--tx': '#fafafa',
      '--mu': '#a1a1aa',
      '--bd': '#27272a',
      '--pr': '#fafafa',
      '--on': '#09090b',
      '--ok': '#34d399',
      '--wn': '#fbbf24',
      '--er': '#f87171',
      '--ac': '#34d399',
      '--code': '#0c0c0f',
      '--k': '#7dd3fc',
      '--s': '#fcd34d',
      '--n': '#c4b5fd',
    });
    expect(declared.get('--r')).toBe('8px');
  });

  it('pins the light palette byte for byte', () => {
    expect(Object.fromEntries(lightPalette)).toMatchObject({
      '--bg': '#fafafa',
      '--sf': '#fff',
      '--sf2': '#f4f4f5',
      '--tx': '#18181b',
      '--mu': '#71717a',
      '--bd': '#e4e4e7',
      '--pr': '#18181b',
      '--on': '#fafafa',
      '--ok': '#059669',
      '--wn': '#b45309',
      '--er': '#dc2626',
      '--ac': '#059669',
      '--code': '#f4f4f5',
      '--k': '#0369a1',
      '--s': '#b45309',
      '--n': '#6d28d9',
    });
  });

  it('does not collapse info onto success in either palette', () => {
    // The first direction-C pass aliased --color-info* to --ac, and --ac equals --ok, so
    // every neutral notice rendered in the success green. The info family now derives from
    // the cyan --k; this fails if the two hues become the same value again.
    expect(darkPalette.get('--k')).not.toBe(darkPalette.get('--ok'));
    expect(lightPalette.get('--k')).not.toBe(lightPalette.get('--ok'));
  });

  it('declares the light palette for the OS media query as well as the explicit attribute', () => {
    // Without the media branch the first paint would be dark on a light OS and flash
    // when useTheme() runs; without the attribute branch the toggle could not override
    // the OS. Both are required, and both must carry the same values.
    const mediaPalette = declaredIn(":root:not([data-theme='dark'])");
    for (const [token, value] of lightPalette) {
      expect(mediaPalette.get(token), `${token} missing from the prefers-color-scheme branch`).toBe(value);
    }
  });

  it('keeps the editor tokens dark and theme-invariant (CodeMirror is dark in both)', () => {
    // Mapping these onto the palette would make the editor surface follow the theme while
    // the CodeMirror theme (syntax colours, gutter) stayed dark: a light-on-light editor.
    expect(declared.get('--editor-active-line')).toBe('#1e293b80');
    expect(declared.get('--editor-gutter-surface')).toBe('#111827');
    expect(declared.get('--editor-text')).toBe('#e2e8f0');
  });

  // The retired sizes must not come back: their reappearance would mean someone
  // reintroduced a one-off size instead of using the scale (the defect AC-180-09
  // cleaned up). TASK-394 added exactly three named steps for direction C — the page
  // title (h1 22px), the section title (h2 15px) and the stat value (28px) — so the
  // list is the surviving scale, not a prohibition on ever adding a step.
  it('keeps the type scale to the body/display steps plus the three named direction-C steps', () => {
    const sizeTokens = [...declared.keys()].filter((token) => token.startsWith('--font-size-')).sort();
    expect(sizeTokens).toEqual([
      '--font-size-2xl',
      '--font-size-3xl',
      '--font-size-base',
      '--font-size-h1',
      '--font-size-h2',
      '--font-size-lg',
      '--font-size-md',
      '--font-size-sm',
      '--font-size-stat',
      '--font-size-xl',
      '--font-size-xs',
    ]);
  });

  it('pins the direction-C heading and stat sizes', () => {
    expect(declared.get('--font-size-h1')).toBe('1.375rem'); // 22px
    expect(declared.get('--font-size-h2')).toBe('0.9375rem'); // 15px
    expect(declared.get('--font-size-stat')).toBe('1.75rem'); // 28px
    expect(declared.get('--font-weight-heading')).toBe('650');
  });

  it('pins the body steps the console actually renders', () => {
    expect(declared.get('--font-size-xs')).toBe('0.75rem');
    expect(declared.get('--font-size-sm')).toBe('0.8125rem');
    expect(declared.get('--font-size-md')).toBe('0.875rem');
    expect(declared.get('--font-size-base')).toBe('1rem');
    expect(declared.get('--font-size-lg')).toBe('1.125rem');
  });

  // The rule in tokens.css ("no raw font-size") had no gate: mutation testing by
  // the independent review wrote `font-size: 0.9rem` into a component and every
  // test stayed green.
  it('never writes a raw font-size in any source file', () => {
    const offenders: string[] = [];
    for (const file of files) {
      const text = readFileSync(file, 'utf8');
      for (const match of text.matchAll(/font-size\s*:\s*([^;{}]+)/g)) {
        const value = match[1]!.trim();
        if (!value.startsWith('var(--font-size-')) offenders.push(`${file}: font-size: ${value}`);
      }
      for (const match of text.matchAll(/fontSize\s*:\s*'([^']+)'/g)) {
        const value = match[1]!.trim();
        if (!value.startsWith('var(--font-size-')) offenders.push(`${file}: fontSize: ${value}`);
      }
    }
    expect(offenders).toEqual([]);
  });

  it('only uses scale tokens for font sizes in the migrated dialogs', () => {
    // Before AC-180-09 these three dialogs used 0.85rem (13.6px) and 0.8rem
    // (12.8px); the collapse moved them onto 13px on purpose, reviewed against
    // before/after screenshots. What must stay true is that they use the SCALE and
    // not an ad-hoc size.
    const scale = new Set(['--font-size-xs', '--font-size-sm', '--font-size-md', '--font-size-base', '--font-size-lg', '--font-size-xl']);
    const filesUsingIt = [
      'src/components/emergency/EmergencyConfirmDialog.vue',
      'src/components/values/RejectRevisionDialog.vue',
      'src/components/operations/CancelOperationDialog.vue',
    ];
    const tokens: string[] = [];
    for (const file of filesUsingIt) {
      const text = readFileSync(resolve(webRoot, file), 'utf8');
      for (const match of text.matchAll(/font-size:\s*var\((--font-size-[a-z-]+)\)/g)) {
        tokens.push(match[1]!);
      }
    }
    expect(tokens.length).toBeGreaterThan(0);
    for (const token of tokens) expect(scale.has(token), `${token} is not a scale step`).toBe(true);
  });
});
