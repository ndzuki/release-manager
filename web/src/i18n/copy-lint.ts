/*
 * Copy-lint helpers for the W5 hygiene gate (plan §11 W5).
 *
 * Kept in a module of their own so they can be unit-tested directly: the gate built
 * on them is only as good as its detector, and the first version silently missed
 * user-facing copy that lives in attributes or in `<script setup>`.
 *
 * Scope, stated explicitly because a gate's blind spots are part of its contract:
 *  - template TEXT nodes (comments stripped)
 *  - template ATTRIBUTE values that render to the user (aria-label/placeholder/title/alt/label)
 *  - `<script>` string literals that look like a SENTENCE (contain whitespace and at
 *    least two letter runs) — this is what catches rendered messages like
 *    'Unable to switch organization.' while ignoring identifiers, CSS classes,
 *    route names and import paths.
 * Out of scope: anything assembled at runtime (template literals, concatenation).
 */

const RENDERED_ATTRIBUTES = ['aria-label', 'placeholder', 'title', 'alt', 'label', 'message', 'description'];

/** Technical attributes whose values are never user-facing copy. */
const NON_COPY_ATTRIBUTES = new Set([
  'class', 'style', 'id', 'for', 'name', 'type', 'role', 'key', 'ref', 'href', 'src', 'to',
  'as', 'scope', 'method', 'action', 'rel', 'target', 'autocomplete', 'inputmode', 'pattern',
  'maxlength', 'minlength', 'step', 'min', 'max', 'd', 'viewBox', 'fill', 'stroke', 'xmlns',
  'slot', 'lang', 'dir', 'tabindex', 'autofocus',
]);

/**
 * Removes comments in one forward scan.
 *
 * A regex stripper is the obvious implementation and the wrong one: `<!--[\s\S]*?-->`
 * leaves an unterminated `<!--` behind, and the same holds for a block comment -- the
 * class of defect CodeQL reports as js/incomplete-multi-character-sanitization. The scan
 * consumes an unterminated comment to the end of the input, which is what a lexer does
 * with the same input, so no marker can survive into the text this detector inspects.
 */
export function stripComments(text: string): string {
  let scanned = '';
  let index = 0;
  while (index < text.length) {
    if (text.startsWith('<!--', index)) {
      const end = text.indexOf('-->', index + 4);
      index = end === -1 ? text.length : end + 3;
      continue;
    }
    if (text.startsWith('/*', index)) {
      const end = text.indexOf('*/', index + 2);
      index = end === -1 ? text.length : end + 2;
      continue;
    }
    scanned += text[index];
    index += 1;
  }
  return scanned
    // Only a line comment: `//` that is not part of a URL AND whose line carries no
    // string quote before it. The first version ate everything after an inline `//`,
    // including copy that lived inside a string on the same line.
    .split('\n')
    .map((line) => {
      const index = line.indexOf('//');
      if (index === -1 || line.slice(0, index).includes("'") || line.slice(0, index).includes('"')) return line;
      return line.slice(0, index);
    })
    .join('\n');
}

/**
 * Identifiers share the syntax with copy: Vue event names (`update:modelValue`),
 * camelCase/kebab object keys (`sourcePrefix`), enum values, and route names. They are
 * never shown to a user, so treating them as copy would only produce noise.
 */
export function isIdentifier(literal: string): boolean {
  // Deliberately narrow: a bare lowerCamelCase word is ambiguous (an object key such as
  // `sourcePrefix` looks exactly like copy such as `iPhone`), so those are reported and
  // their identifier positions are stripped contextually instead (see stripIdentifierPositions).
  return (
    /^[a-z]+:[A-Za-z]+$/.test(literal) ||
    /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/.test(literal) ||
    /^[a-z][A-Za-z0-9]*(?:\.[A-Za-z0-9]+)+$/.test(literal) ||
    /^[A-Z][A-Z0-9_]+$/.test(literal)
  );
}

/** Removes the syntactic positions where a literal is definitely an identifier. */
function stripIdentifierPositions(text: string): string {
  return text
    .replace(/\bfieldError\([^)]*\)/g, ' ')
    .replace(/\.field\s*===\s*'[^']*'/g, ' ')
    .replace(/\bname:\s*'[^']*'/g, ' ')
    .replace(/\bparams:\s*\{[^}]*\}/g, ' ')
    .replace(/t\(\s*'[^']*'\s*\)/g, ' ')
    // A domain argument is an identifier, not copy: statusLabel('operation', state).
    .replace(/statusLabel\(\s*'[^']*'\s*,/g, 'statusLabel(');
}

function sections(text: string): { template: string; script: string } {
  const template = /<template[^>]*>([\s\S]*)<\/template\s*>/i.exec(text)?.[1];
  const script = /<script[^>]*>([\s\S]*?)<\/script\s*>/i.exec(text)?.[1];
  // A plain .ts module has neither tag: treat the whole file as script, otherwise the
  // helper modules that render user copy (utils/*.ts) are invisible to this detector.
  if (template === undefined && script === undefined) return { template: '', script: text };
  return { template: template ?? '', script: script ?? '' };
}

/**
 * Literal text shown by the template. Interpolations are removed rather than used to
 * exclude the whole node: `<p>Sign out {{ name }}</p>` hides real copy, and the first
 * version of this detector skipped any node containing braces.
 */
export function templateLiterals(text: string): string[] {
  // Attribute values are not text nodes. They must go first: an arrow function inside a
  // bound attribute (`v-if="…find((x) => x.field === 'a')"`) otherwise looks like a tag
  // close, and the text after its `>` was reported as copy.
  const template = stripComments(sections(text).template)
    .replace(/\s[\w:@.-]+\s*=\s*"[^"]*"/g, ' ')
    .replace(/\s[\w:@.-]+\s*=\s*'[^']*'/g, ' ')
    // Interpolations must go BEFORE text nodes are split: an arrow function inside one
    // (`find((x) => x.field === 'a')`) contains a `>` that would otherwise cut the node
    // in half and leave the tail looking like copy.
    .replace(/\{\{[\s\S]*?\}\}/g, ' ');
  const literals: string[] = [];
  for (const match of template.matchAll(/>([^<>]*)</gu)) {
    const literal = match[1]!.replace(/\s+/g, ' ').trim();
    if (literal && /\p{L}{2,}/u.test(literal)) literals.push(literal);
  }
  return literals;
}

/**
 * Rendered attribute values.
 *
 * The first version only looked at a hand-written name list, so `action-label="Retry"`
 * slipped through: the name was not on the list and the text-node scan strips
 * attributes. Any non-bound attribute whose value reads like copy is reported now, and
 * the known rendering props are still checked even when their value is short.
 */
export function attributeLiterals(text: string): string[] {
  const template = stripComments(sections(text).template);
  const literals: string[] = [];
  const looksLikeCopy = (value: string): boolean =>
    /\p{Lu}/u.test(value) || /[.!?。！？]$/u.test(value) || value.trim().includes(' ');
  // Only plain attributes: `:prop="expr"`, `@event="handler"` and `v-if` are expressions,
  // and their values are covered by templateExpressionLiterals instead.
  for (const match of template.matchAll(/\s((?!v-)[a-zA-Z][\w-]*)\s*=\s*"([^"{][^"]*)"/g)) {
    const [, name, value] = match as unknown as [string, string, string];
    const known = RENDERED_ATTRIBUTES.includes(name);
    const technical = NON_COPY_ATTRIBUTES.has(name) || name.startsWith('data-');
    if (technical) continue;
    if (value.trim() && (known || looksLikeCopy(value))) literals.push(value.trim());
  }
  // Bound attributes can carry a literal too: :title="'Sign out'".
  for (const match of template.matchAll(/:\w[\w-]*="\s*'([^']+)'\s*"/g)) {
    const literal = match[1]!.trim();
    if (literal && /\p{L}{2,}/u.test(literal)) literals.push(literal);
  }
  return literals;
}

/** String literals in <script> that read as a sentence (user-facing copy). */
export function scriptSentences(text: string): string[] {
  const script = stripComments(sections(text).script);
  const sentences: string[] = [];
  for (const match of script.matchAll(/'([^'\\\n]*)'|"([^"\\\n]*)"/g)) {
    const literal = (match[1] ?? match[2] ?? '').trim();
    if (isIdentifier(literal)) continue;
    if (literal.includes(' ') && (literal.match(/[\p{L}]{2,}/gu)?.length ?? 0) >= 2) sentences.push(literal);
  }
  // Template literals are just as renderable; single words are too noisy to flag.
  for (const match of script.matchAll(/`([^`\\]*)`/g)) {
    const literal = match[1]!.trim();
    if (literal.includes(' ') && (literal.match(/[\p{L}]{2,}/gu)?.length ?? 0) >= 2) sentences.push(literal);
  }
  return sentences;
}

/**
 * Quoted string literals inside the template — `{{ cond ? 'A' : 'B' }}` and
 * `:title="'A'"`. These render like any other copy but are invisible to a text-node
 * scan, which is how `Tenant boundary for release management.` slipped through.
 */
export function templateExpressionLiterals(text: string): string[] {
  // Identifiers live in the same expression syntax as copy, so strip them first:
  // catalog lookups t('key'), vue-router route names (name: 'Home'), and params.
  const template = stripIdentifierPositions(stripComments(sections(text).template));
  const literals: string[] = [];
  for (const match of template.matchAll(/'([^'\n]+)'/g)) {
    const literal = match[1]!.trim();
    // A class list (`status status--active`) is not copy: require a capitalised word or
    // sentence punctuation before treating a quoted expression as user-facing text.
    const looksLikeCopy = /\p{Lu}/u.test(literal) || /[.!?。！？]$/u.test(literal);
    // A capitalised single word is still copy ("Active", "Disabled"); a CSS class list
    // or an identifier is not, which is why those tests come first.
    if (looksLikeCopy && /\p{L}{2,}/u.test(literal) && !isIdentifier(literal)) literals.push(literal);
  }
  return literals;
}

/**
 * Capitalised single words in <script> — display labels such as `Draft`, `Added`,
 * `Superseded`, usually held in a lookup object. The sentence rule cannot see them
 * (it requires whitespace) and identifiers never look like this (they start lowercase),
 * so the rule is narrow enough to report without noise.
 */
export function scriptWordLiterals(text: string): string[] {
  const script = stripIdentifierPositions(stripComments(sections(text).script));
  const literals: string[] = [];
  for (const match of script.matchAll(/'([A-Z][a-z]{2,})'|"([A-Z][a-z]{2,})"/g)) {
    literals.push((match[1] ?? match[2])!);
  }
  return literals;
}

/** Everything a migrated file must have moved into the catalog. */
export function bareCopy(text: string): string[] {
  return [
    ...templateLiterals(text),
    ...attributeLiterals(text),
    ...templateExpressionLiterals(text),
    ...scriptSentences(text),
    ...scriptWordLiterals(text),
  ];
}

/**
 * Message keys referenced through `t('key')`. Comments are stripped first: the first
 * version of this scan ran on the raw file, so a comment like
 * `// t('action.refresh') planned` counted as a consumer and a dead entry stayed
 * green. Quoted forms only — a dynamic `t(key)` cannot be verified statically.
 */
export function referencedKeys(text: string): string[] {
  const withoutComments = stripComments(text);
  const keys: string[] = [];
  for (const match of withoutComments.matchAll(/(?<![\w$])t\(\s*'([^']+)'/g)) keys.push(match[1]!);
  return keys;
}
