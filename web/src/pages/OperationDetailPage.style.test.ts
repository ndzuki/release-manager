import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

/*
 * TASK-394: happy-dom has no layout engine, so this regression cannot be observed in a
 * component test — the defect needs a real browser to manifest (a compact-JSON "word" of
 * ~2030px inside an `auto` grid track widened the single column and every sibling, and
 * the document measured 2190 at a 1440 viewport). These assertions pin the declarations
 * that removed it, so deleting one is a test failure instead of a silently overflowing
 * page; the mechanism itself was verified in Chromium at 1440 for the TASK-394 report.
 */
const page = readFileSync(resolve(__dirname, 'OperationDetailPage.vue'), 'utf8');
const panel = readFileSync(resolve(__dirname, '../components/operations/PreflightResultPanel.vue'), 'utf8');
const banner = readFileSync(resolve(__dirname, '../components/operations/DisconnectBanner.vue'), 'utf8');

describe('operation detail overflow guards (TASK-394)', () => {
  it('gives the page grid a zero-minimum track and zero-minimum items', () => {
    // An implicit `auto` track took its minimum from the widest child's min-content; the
    // explicit `minmax(0, 1fr)` plus `min-width: 0` is what keeps the column at the shell
    // width. Either one alone is not enough (a zero track still lets content paint out of
    // an item whose own automatic minimum is min-content).
    expect(page).toContain('grid-template-columns: minmax(0, 1fr)');
    expect(page).toContain('.operation-detail > * { min-width: 0; }');
  });

  it('keeps the preflight detail out of the page flow and scrollable inside itself', () => {
    // Raw JSON goes into a collapsed <details> (no contents rendered while closed), on its
    // own flex line; long plain-text details break with `anywhere` (the only wrap keyword
    // that also lowers min-content), and the expanded <pre> scrolls inside the panel.
    expect(panel).toContain('<details');
    expect(panel).toContain('flex-basis: 100%');
    expect(panel).toContain('overflow-wrap: anywhere');
    expect(panel).toContain('max-width: 100%');
    expect(panel).toContain('overflow: auto');
  });

  it('makes the disconnect banner safe against a wide neighbour', () => {
    expect(banner).toContain('min-width: 0');
    expect(banner).toContain('overflow-wrap: anywhere');
  });
});
