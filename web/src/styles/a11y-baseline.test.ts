import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

/*
 * TASK-269 subset guard, parts 3 and 4: a visible `:focus-visible` ring and a
 * `prefers-reduced-motion` fallback are console-wide properties of the baseline
 * stylesheet, so they are asserted on the stylesheet text. happy-dom does not evaluate
 * `:focus-visible` or media queries, and a component test therefore cannot falsify them.
 *
 * The regression this pins is real, not hypothetical. AuditEventTable.vue used to share
 * one rule between hover and focus:
 *
 *   .audit-results__row:hover,
 *   .audit-results__row:focus-visible { background: var(--color-info-soft); outline: none; }
 *
 * A scoped `.audit-results__row:focus-visible` (0,2,0) beats base.css's global
 * `:focus-visible` (0,1,0), so that `outline: none` silently removed the keyboard focus
 * ring from a `tabindex="0"` row. No test in the suite could see it; this one can.
 *
 * Two invariants, both mechanical:
 *   1. the baseline declares the ring from tokens, and the motion fallback overrides
 *      durations (it needs `!important` — a component's `animation:` shorthand on a
 *      class outranks the universal selector no matter the source order);
 *   2. no stylesheet in src/** switches the outline off INSIDE a `:focus-visible` rule.
 *      `:focus:not(:focus-visible)` is the one legitimate negative form (it suppresses
 *      the ring for mouse clicks only) and is excluded.
 */

const webRoot = (() => {
  for (const candidate of ['.', 'web']) {
    try {
      statSync(resolve(process.cwd(), candidate, 'src/styles/base.css'));
      return candidate;
    } catch {
      // next candidate
    }
  }
  throw new Error(`base.css not found from ${process.cwd()}`);
})();

const baseCss = readFileSync(resolve(webRoot, 'src/styles/base.css'), 'utf8');

interface CssBlock {
  selector: string;
  body: string;
  file: string;
  line: number;
}

/**
 * Flat `selector { body }` pairs. Comments are blanked (keeping newlines so reported
 * lines stay true) and at-rule headers are skipped, which is enough for the two shapes
 * asserted here: a top-level `:focus-visible` rule and the rules nested in the
 * reduced-motion media query.
 */
function cssBlocks(text: string, file: string): CssBlock[] {
  const blanked = text.replace(/\/\*[\s\S]*?\*\//g, (comment) => comment.replace(/[^\n]/g, ' '));
  const blocks: CssBlock[] = [];
  for (const match of blanked.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selector = match[1]!.trim();
    if (!selector || selector.startsWith('@')) continue;
    blocks.push({
      selector,
      body: match[2]!,
      file,
      line: blanked.slice(0, match.index).split('\n').length,
    });
  }
  return blocks;
}

/** Body of the first `{...}` after `index`, with nesting respected. */
function blockAfter(text: string, index: number): string {
  const start = text.indexOf('{', index);
  if (start === -1) return '';
  let depth = 0;
  for (let cursor = start; cursor < text.length; cursor += 1) {
    if (text[cursor] === '{') depth += 1;
    else if (text[cursor] === '}') {
      depth -= 1;
      if (depth === 0) return text.slice(start + 1, cursor);
    }
  }
  return '';
}

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const entry of readdirSync(dir)) {
    if (entry === 'gen' || entry === 'node_modules') continue;
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) sourceFiles(path, out);
    else if (/\.(vue|css)$/.test(entry)) out.push(path);
  }
  return out;
}

describe('focus policy', () => {
  it('draws the global focus ring from tokens', () => {
    const ring = cssBlocks(baseCss, 'base.css').filter((block) => block.selector === ':focus-visible');

    expect(ring, 'base.css must declare exactly one global :focus-visible ring').toHaveLength(1);
    expect(ring[0]!.body).toContain('outline: 2px solid var(--color-primary)');
    expect(ring[0]!.body).toContain('box-shadow: var(--focus-ring)');
  });

  it('gives the standalone baseline the same ring, so the primitive and the page agree', () => {
    // FormField restates the ring for its slotted control: a scoped rule cannot reach
    // slot content without :slotted(), so dropping this would take the ring away from
    // every field the primitive owns.
    const formField = readFileSync(resolve(webRoot, 'src/components/common/FormField.vue'), 'utf8');

    expect(formField).toContain(':slotted(input:focus-visible)');
    expect(formField).toContain('outline: 2px solid var(--color-primary)');
    expect(formField).toContain('box-shadow: var(--focus-ring)');
  });

  it('never switches the outline off inside a :focus-visible rule', () => {
    const offenders: string[] = [];
    for (const file of sourceFiles(resolve(webRoot, 'src'))) {
      for (const block of cssBlocks(readFileSync(file, 'utf8'), file)) {
        if (!block.selector.includes(':focus-visible')) continue;
        // The negative form targets the mouse-click case on purpose.
        if (block.selector.includes(':not(:focus-visible)')) continue;
        if (/(^|[\s;{])outline\s*:\s*(none|0)\s*(;|$|\})/.test(block.body)) {
          offenders.push(`${relative(webRoot, file)}:${block.line} ${block.selector}`);
        }
      }
    }

    expect(offenders, 'a :focus-visible rule must not cancel the focus indicator').toEqual([]);
  });
});

describe('reduced motion policy', () => {
  const mediaIndex = baseCss.indexOf('@media (prefers-reduced-motion: reduce)');

  it('declares a global reduced-motion fallback', () => {
    expect(mediaIndex, 'base.css must carry the console-wide motion fallback').toBeGreaterThan(-1);
  });

  it('overrides every animation and transition duration', () => {
    const body = blockAfter(baseCss, mediaIndex);

    // !important is load-bearing: LoadingState's `.spinner { animation: spin 0.6s … }`
    // is a class rule and outranks `* { animation-duration }` whatever the order.
    expect(body).toMatch(/animation-duration:\s*0\.01ms\s*!important/);
    expect(body).toMatch(/animation-iteration-count:\s*1\s*!important/);
    expect(body).toMatch(/transition-duration:\s*0\.01ms\s*!important/);
    expect(body).toMatch(/scroll-behavior:\s*auto\s*!important/);
  });
});
