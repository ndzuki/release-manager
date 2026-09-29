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

  // literal → token, as substituted by the migration. Every pair is a rendered
  // value that existed before the migration.
  const EQUIVALENT_PAIRS: Record<string, string> = {
    '#64748b': '--color-muted',
    '#e2e8f0': '--color-border',
    '#cbd5e1': '--color-border-strong',
    '#0f172a': '--color-text',
    '#2563eb': '--color-primary',
    '#b91c1c': '--color-error',
    '#94a3b8': '--color-subtle',
    '#f8fafc': '--color-bg',
    '#ffffff': '--color-on-accent',
    '#334155': '--color-text-secondary',
    '#475569': '--color-muted-strong',
    '#dc2626': '--color-danger',
    '#ef4444': '--color-danger-border',
    '#991b1b': '--color-error-strong',
    '#92400e': '--color-warning-ink-strong',
    '#fffbeb': '--color-warning-surface',
    '#f59e0b': '--color-warning-solid',
    '#fef3c7': '--color-warning-subtle',
    '#bfdbfe': '--color-info-border-soft',
    '#93c5fd': '--color-info-border',
    '#eff6ff': '--color-info-soft',
    '#f0fdf4': '--color-success-surface-soft',
    '#dcfce7': '--color-success-surface',
    '#166534': '--color-success-ink',
    '#1e293b80': '--editor-active-line',
    '#111827': '--editor-gutter-surface',
    // AC-180-09 collapsed the 16-size scale onto five body steps plus three
    // display steps; these pins are the surviving scale.
  };

  it.each(Object.entries(EQUIVALENT_PAIRS))('keeps %s as %s', (literal, token) => {
    expect(declared.get(token)).toBe(literal);
  });

  // The retired sizes must not come back: their reappearance would mean someone
  // reintroduced a one-off size instead of using the scale (the defect AC-180-09
  // cleaned up).
  it('keeps the type scale collapsed to the five body steps plus display steps', () => {
    const sizeTokens = [...declared.keys()].filter((token) => token.startsWith('--font-size-')).sort();
    expect(sizeTokens).toEqual([
      '--font-size-2xl',
      '--font-size-3xl',
      '--font-size-base',
      '--font-size-lg',
      '--font-size-md',
      '--font-size-sm',
      '--font-size-xl',
      '--font-size-xs',
    ]);
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
