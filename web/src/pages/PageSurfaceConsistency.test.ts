import { readFileSync } from 'node:fs';
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
