import { globSync, readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

/*
 * Locator hygiene for the end-to-end specs.
 *
 * Every e2e locator that depends on USER-VISIBLE TEXT is a coupling to the console's
 * copy: when a surface converges to Chinese, the assertion breaks — or worse, keeps
 * passing while asserting nothing.
 *
 * What this gate checks: a locator that carries ASCII letters must be recorded with a
 * reason, and a `rendered` record must still find its text somewhere in src (i.e. the
 * page really does render it). That second half is what catches the case this gate was
 * written for: after a surface converges, an English locator stops existing in src and
 * fails here even though no spec file changed.
 *
 * What it does NOT check (stated so nobody trusts it further than it goes): it cannot
 * prove that a CHINESE locator matches the rendered copy, and it does not run the spec.
 * A converged page whose Chinese wording changes still needs a real e2e run.
 */
const e2eRoot = resolve(__dirname, '../../e2e');

interface RecordedLocator {
  spec: string;
  literal: string;
  reason: string;
  /**
   * `rendered` — the console still renders this text (the gate verifies it appears in src).
   * `fallback` — an alternate half of a regex locator kept so the spec works against an
   *              environment that still serves the unconverged login copy.
   */
  kind: 'rendered' | 'fallback';
}

const RECORDED: RecordedLocator[] = [
  {
    spec: 'emergency-smoke.spec.ts',
    literal: '保存 Draft',
    kind: 'rendered',
    reason: 'the button keeps the English domain noun Draft inside Chinese copy',
  },
  {
    spec: 'emergency-smoke.spec.ts',
    literal: 'Username',
    kind: 'fallback',
    reason: 'fallback half of /用户名|Username/ for environments still serving the English login',
  },
  {
    spec: 'emergency-smoke.spec.ts',
    literal: 'Password',
    kind: 'fallback',
    reason: 'fallback half of /密码|Password/',
  },
  {
    spec: 'emergency-smoke.spec.ts',
    literal: 'Login',
    kind: 'fallback',
    reason: 'fallback half of /登录|Login|Sign in/i',
  },
  {
    spec: 'emergency-smoke.spec.ts',
    literal: 'Sign in',
    kind: 'fallback',
    reason: 'fallback half of /登录|Login|Sign in/i',
  },
  {
    spec: 'emergency-smoke.spec.ts',
    literal: 'DEPLOYMENT',
    kind: 'fallback',
    reason: 'target kind taken from the server enum; not copy, and matched case-insensitively',
  },
  {
    spec: 'navigation.spec.ts',
    literal: 'Username',
    kind: 'fallback',
    reason: 'fallback half of /用户名|Username/ for environments still serving the English login',
  },
  {
    spec: 'navigation.spec.ts',
    literal: 'Password',
    kind: 'fallback',
    reason: 'fallback half of /密码|Password/',
  },
  {
    spec: 'navigation.spec.ts',
    literal: 'Login',
    kind: 'fallback',
    reason: 'fallback half of /登录|Login|Sign in/i',
  },
  {
    spec: 'navigation.spec.ts',
    literal: 'Sign in',
    kind: 'fallback',
    reason: 'fallback half of /登录|Login|Sign in/i',
  },
  {
    spec: 'emergency-smoke.spec.ts',
    literal: 'Login',
    kind: 'fallback',
    reason: 'fallback half of /登录|Login|Sign in/i',
  },
];

function locatorLiterals(): Array<{ spec: string; literal: string }> {
  const found: Array<{ spec: string; literal: string }> = [];
  for (const file of globSync(`${e2eRoot}/*.spec.ts`)) {
    const spec = file.split('/').pop()!;
    const text = readFileSync(file, 'utf8').replace(/\/\/[^\n]*/g, '');
    const patterns = [
      /\bname:\s*'([^']+)'/g,
      /\bgetByLabel\(\s*'([^']+)'/g,
      /\bgetByText\(\s*'([^']+)'/g,
      /\bgetByTestId\(\s*'([^']+)'/g,
      // Regex locators carry copy too: /用户名|Username/ has an English half.
      /\/([^/\n]+)\/[a-z]*/g,
    ];
    for (const pattern of patterns) {
      for (const match of text.matchAll(pattern)) {
        // An alternation is several locators; each half is checked on its own.
        for (const part of match[1]!.split('|')) {
          // Drop regex syntax (including escapes like \s) before judging the text.
          const literal = part.trim().replace(/\\[A-Za-z]/g, ' ').replace(/[$^.*+?()[\]{}]/g, '').trim();
          if (!/[A-Za-z\u4e00-\u9fff]{2,}/.test(literal)) continue;
          if (/^[a-z][\w-]*$/.test(literal)) continue;
          // Pure-Chinese locators match the target locale. Anything carrying ASCII
          // letters — including mixed text like `Prepare 收敛` — is a coupling to copy
          // that may still change, so it must be recorded.
          if (/[\u4e00-\u9fff]/.test(literal) && !/[A-Za-z]/.test(literal)) continue;
          found.push({ spec, literal });
        }
      }
    }
  }
  return found;
}

describe('e2e locator hygiene', () => {
  it('records every locator that carries ASCII text', () => {
    const recorded = new Set(RECORDED.map((entry) => `${entry.spec}:${entry.literal}`));
    const unrecorded = locatorLiterals()
      .map((entry) => `${entry.spec}:${entry.literal}`)
      .filter((key) => !recorded.has(key));

    expect(
      [...new Set(unrecorded)],
      'these e2e locators depend on English UI text: record them with a reason, or converge the copy',
    ).toEqual([]);
  });

  it('has no stale record', () => {
    const actual = new Set(locatorLiterals().map((entry) => `${entry.spec}:${entry.literal}`));
    const stale = RECORDED.map((entry) => `${entry.spec}:${entry.literal}`).filter((key) => !actual.has(key));

    expect(stale, 'these locator records no longer match any locator (the copy moved)').toEqual([]);
  });

  // The point of the manifest: an English locator must be a deliberate, explained
  // exception rather than an unnoticed coupling to copy that has already converged.
  // A `rendered` record claims the console still shows that text: verify it. This is the
  // check that fails once a surface converges, even though no spec file changed.
  it('verifies that rendered locators still exist in src', () => {
    const sources = globSync(resolve(__dirname, '../**/*.{vue,ts}'))
      .filter((file) => !file.includes('.test.') && !file.includes('.spec.'))
      .map((file) => readFileSync(file, 'utf8'))
      .join('\n');
    const missing = RECORDED.filter(
      (entry) => entry.kind === 'rendered' && !sources.includes(entry.literal),
    ).map((entry) => `${entry.spec}:${entry.literal}`);

    expect(missing, 'these locators claim the console renders English that no longer exists in src').toEqual([]);
  });

  it('requires a reason for every English locator', () => {
    const withoutReason = RECORDED.filter(
      (entry) => !/[\u4e00-\u9fff]/.test(entry.literal) && entry.reason.trim().length < 10,
    );

    expect(withoutReason.map((entry) => entry.literal)).toEqual([]);
  });
});
