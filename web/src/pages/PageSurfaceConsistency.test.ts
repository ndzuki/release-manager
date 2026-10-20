import { globSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

/*
 * W2 (TASK-394): the page-level half of the direction-C consistency pass over
 * OperatorList / OperatorDetail / OperatorEnroll / LocalUsers / ChangePassword.
 *
 * happy-dom does not evaluate scoped CSS, so these assertions read the SFC sources —
 * the same convention OperatorTable.test.ts and Toast.test.ts use for their stylesheet
 * invariants. They pin the mechanical part (which token paints which box); the runtime
 * half (headings, UUIDs, breadcrumbs, widths) lives in each page's own test file.
 *
 * The direction-C target these encode:
 *   card   → background var(--sf/--color-surface) + 1px var(--color-border-strong) + var(--r)
 *   field  → muted-surface fill + the same strong border
 *   table  → frame on the scroller, thead on --color-surface-muted / --color-muted-strong
 *   select → appearance:none, caret drawn from --color-muted
 *   page   → no second padding on top of the shell's <main>
 */
const PAGES = [
  'OperatorListPage.vue',
  'OperatorDetailPage.vue',
  'OperatorEnrollPage.vue',
  'LocalUsersPage.vue',
  'ChangePasswordPage.vue',
];

function sourceOf(file: string): string {
  return readFileSync(resolve(__dirname, file), 'utf8');
}

function styleOf(file: string): string {
  const withoutComments = sourceOf(file).replace(/\/\*[\s\S]*?\*\//g, '');
  const match = withoutComments.match(/<style scoped>([\s\S]*?)<\/style>/);
  if (!match) throw new Error(`${file} has no scoped style block`);
  return match[1]!;
}

function templateOf(file: string): string {
  const match = sourceOf(file).match(/<template>([\s\S]*)<\/template>/);
  if (!match) throw new Error(`${file} has no template block`);
  return match[1]!;
}

/** The body of one flat `selector { … }` rule, comments already stripped. */
function rule(style: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = style.match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`));
  if (!match) throw new Error(`rule "${selector}" not found`);
  return match[1]!;
}

describe('W2 page surfaces (TASK-394)', () => {
  it.each(PAGES)('%s delegates its heading to PageHeader instead of hand-rolling it', (file) => {
    const template = templateOf(file);

    expect(template).toContain('<PageHeader');
    expect(template).not.toMatch(/class="eyebrow"/);
    expect(template).not.toMatch(/<h1[\s>]/);
    // No page-local heading chrome survives the migration.
    expect(styleOf(file)).not.toMatch(/\.eyebrow\s*\{/);
  });

  it('paints the operator detail card with a surface, a strong border and the direction-C radius', () => {
    const card = rule(styleOf('OperatorDetailPage.vue'), '.card');

    expect(card).toContain('background: var(--color-surface)');
    expect(card).toContain('border: 1px solid var(--color-border-strong)');
    expect(card).toContain('border-radius: var(--r)');
  });

  it('paints the enrollment card and its inputs from the card/field tokens', () => {
    const style = styleOf('OperatorEnrollPage.vue');

    const card = rule(style, '.card');
    expect(card).toContain('background: var(--color-surface)');
    expect(card).toContain('border: 1px solid var(--color-border-strong)');
    expect(card).toContain('border-radius: var(--r)');

    const input = rule(style, '.card input');
    expect(input).toContain('background: var(--color-surface-muted)');
    expect(input).toContain('border: 1px solid var(--color-border-strong)');
  });

  it('paints the operator filter row as a card', () => {
    const filters = rule(styleOf('../components/operators/OperatorFilters.vue'), '.filters');

    expect(filters).toContain('background: var(--color-surface)');
    expect(filters).toContain('border: 1px solid var(--color-border-strong)');
    expect(filters).toContain('border-radius: var(--r)');
  });

  it('frames the local-users table and paints its header from the direction-C tokens', () => {
    const style = styleOf('LocalUsersPage.vue');

    const frame = rule(style, '.table-wrap');
    expect(frame).toContain('border: 1px solid var(--color-border-strong)');
    expect(frame).toContain('background: var(--color-surface)');
    expect(frame).toContain('border-radius: var(--r)');

    const header = rule(style, '.local-users__table th');
    expect(header).toContain('background: var(--color-surface-muted)');
    expect(header).toContain('color: var(--color-muted-strong)');
  });

  it('customises the local-users select instead of leaving the UA widget', () => {
    const style = styleOf('LocalUsersPage.vue');

    const select = rule(style, '.select-wrap select');
    expect(select).toContain('appearance: none');
    expect(select).toContain('width: 100%');
    expect(style).toMatch(/\.select-wrap__caret\s*\{[^}]*border-right:\s*1px solid var\(--color-muted\)/);
    // The field fill applies to both the text inputs and the select.
    expect(style).toMatch(
      /\.local-users__create input,\s*\.local-users__create select\s*\{[^}]*background:\s*var\(--color-surface-muted\)/,
    );
  });

  it('does not stack a second page padding on top of the shell', () => {
    // Only ChangePasswordPage used to add `padding: var(--space-5)`; the shell's <main>
    // already owns 24px 32px 48px, so a page-local copy doubled the inset.
    const root = rule(styleOf('ChangePasswordPage.vue'), '.change-password');
    expect(root).not.toContain('padding');
  });
});

/*
 * W7 (TASK-394): the last two page surfaces the earlier waves never reached.
 *
 * EmergencyChangePage was the only routed page with no h1 at all — it hand-rolled a
 * breadcrumb nav and put the page name in a <strong>. ValuesEditorPage carried the last
 * hand-written `.eyebrow` in the page layer (12px / 800 / .08em / uppercase), which is the
 * exact drift the direction-C pass exists to remove.
 */
describe('W7 page surfaces (TASK-394)', () => {
  it('delegates the emergency page heading to PageHeader', () => {
    const template = templateOf('EmergencyChangePage.vue');

    expect(template).toContain('<PageHeader');
    // The category comes from the catalog, not from a page-local eyebrow.
    expect(template).toContain(':eyebrow="t(\'emergency.page.eyebrow\')"');
    expect(template).toContain(':title="t(\'emergency.page.title\')"');
    expect(template).toContain(':crumbs="crumbs"');
    expect(template).not.toMatch(/class="eyebrow"/);
    expect(template).not.toMatch(/<h1[\s>]/);
    // The hand-rolled trail must be gone, not merely hidden.
    expect(template).not.toContain('class="breadcrumbs"');
    expect(styleOf('EmergencyChangePage.vue')).not.toMatch(/\.breadcrumbs\s*\{/);
  });

  it('gives the emergency page root no second padding and no double header margin', () => {
    const style = styleOf('EmergencyChangePage.vue');
    const root = rule(style, '.emergency-page');

    expect(root).not.toContain('padding');
    expect(root).toContain('min-width: 0');
    expect(style).toMatch(/\.emergency-page\s+:deep\(\.page-header\)\s*\{\s*margin-bottom:\s*0;\s*\}/);
  });

  it('paints the emergency callouts and cards with the direction-C tokens', () => {
    const style = styleOf('EmergencyChangePage.vue');
    // A warning callout keeps its warning palette; the radius is the shared one.
    expect(rule(style, '.conflict-block')).toContain('border-radius: var(--r)');
    const notice = rule(style, '.unavailable-notice');
    expect(notice).toContain('border: 1px solid var(--color-border-strong)');
    expect(notice).toContain('background: var(--sf)');
    expect(notice).toContain('border-radius: var(--r)');

    for (const [file, selector] of [
      ['../components/emergency/EmergencyTargetSelector.vue', '.target-card'],
      ['../components/emergency/EmergencyArtifactSelector.vue', '.artifact-card'],
      ['../components/emergency/EmergencyActionSelector.vue', '.action-selector'],
      ['../components/emergency/EmergencyChangeForm.vue', '.policy-fieldset'],
    ] as const) {
      const card = rule(styleOf(file), selector);
      expect(card, file).toContain('background: var(--sf)');
      expect(card, file).toContain('border: 1px solid var(--color-border-strong)');
      expect(card, file).toContain('border-radius: var(--r)');
    }
  });

  it('frames the annotation table and paints its header band', () => {
    const style = styleOf('../components/emergency/EmergencyAnnotationEditor.vue');

    // Horizontal containment is the global `.table-wrap`; the page-level table frame and the
    // quiet header band are the direction-C part.
    const frame = rule(style, '.table-wrap');
    expect(frame).toContain('border: 1px solid var(--color-border-strong)');
    expect(frame).toContain('background: var(--sf)');
    expect(frame).toContain('border-radius: var(--r)');

    const header = rule(style, '.annotation-table th');
    expect(header).toContain('background: var(--color-surface-muted)');
    expect(header).toContain('color: var(--color-muted-strong)');
    expect(header).toContain('font-size: var(--font-size-xs)');
    expect(header).toContain('font-weight: 600');
    expect(header).toContain('padding: 10px 20px');

    const cell = rule(style, '.annotation-table td');
    expect(cell).toContain('padding: 10px 20px');
    expect(cell).toContain('border-top: 1px solid var(--color-border-strong)');
  });

  it('turns the values page eyebrow into a section title', () => {
    const template = templateOf('ValuesEditorPage.vue');
    const style = styleOf('ValuesEditorPage.vue');

    expect(template).not.toMatch(/class="eyebrow"/);
    expect(style).not.toMatch(/\.eyebrow\s*\{/);
    // One title per region: the micro-label is gone, the h2 alone names the panel and stays
    // the `aria-labelledby` target.
    expect(template).not.toContain('class="values-section__title"');
    expect(template).not.toContain("t('values.page.editor')");
    expect(template).toContain('id="values-editor-title"');

    const title = rule(style, '.values-panel__header h2');
    expect(title).toContain('font-size: var(--font-size-h2)');
    expect(title).toContain('font-weight: var(--font-weight-heading)');
    // None of the old micro-label treatment survives.
    expect(title).not.toContain('800');
    expect(title).not.toContain('letter-spacing');
    expect(title).not.toContain('uppercase');
    expect(style).not.toMatch(/text-transform:\s*uppercase/);
  });

  it('leaves no hand-written `.eyebrow` in any page', () => {
    // The console converged on PageHeader's `page-header__eyebrow`; a page-local copy is the
    // drift this wave removes, so the invariant is repo-wide rather than per-file.
    const offenders = globSync(`${__dirname}/*.vue`)
      .filter((file: string) => readFileSync(file, 'utf8').includes('class="eyebrow"'))
      .map((file: string) => file.slice(__dirname.length + 1));

    expect(offenders).toEqual([]);
  });
});
