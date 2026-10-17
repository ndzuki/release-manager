/*
 * Copy-lint helpers for the W5 hygiene gate (plan §11 W5).
 *
 * Kept in a module of their own so they can be unit-tested directly: the gate built
 * on them is only as good as its detector, and the first version silently missed
 * user-facing copy that lives in attributes or in `<script setup>`.
 *
 * Scope, stated explicitly because a gate's blind spots are part of its contract:
 *  - template TEXT nodes (comments stripped). A text node renders by construction, so a
 *    single Han character in one is user copy, not structure: the quantifier/conjunction
 *    fragments `共 {{ n }} 个`, `与`, `个`, `条`, `（{{ n }} 条）` were invisible while the
 *    rule asked for two consecutive letters (TASK-318);
 *  - template ATTRIBUTE values that render to the user. The name list below covers the
 *    native attributes and the component props known to render, and a Han-carrying value
 *    is reported whatever the name, because Chinese copy has no capital and no space for
 *    the shape rule to match (TASK-318);
 *  - template EXPRESSION literals: quoted strings inside `{{ … }}` and inside bound
 *    directives — English by shape (capitalised word or sentence punctuation) and Chinese
 *    by the same Han run the script layer uses; backtick template literals there are
 *    scanned by the Han rule only, because a machine value such as
 *    `operation-row-${row.operationId}` satisfies the shape rule. A backtick literal is a
 *    composed, rendered string, so one Han character is enough there:
 *    `${patch.length} 项` was invisible at two-or-more (TASK-318);
 *  - `<script>` string literals that look like a SENTENCE (contain whitespace and at
 *    least two letter runs) — this is what catches rendered messages like
 *    'Unable to switch organization.' while ignoring identifiers, CSS classes,
 *    route names and import paths;
 *  - `<script>` string literals carrying a run of Han characters, with or without
 *    whitespace — the sentence rule above cannot see Chinese copy, so a label like
 *    '已创建定义' used to pass the gate (regex literals and machine strings excluded,
 *    see blankRegexLiterals / isMachineLiteral).
 * Out of scope: copy assembled by concatenation (`'向 ' + name + ' 发放'`), which has no
 * single literal to report; an interpolated template literal is reported as one literal,
 * not per fragment.
 */

/*
 * Attribute names whose value renders to the user even when the value is short and carries
 * no shape the `looksLikeCopy` heuristic could see (`title="Hi"`).
 *
 * `action-label` is a COMPONENT PROP, not a native attribute: ErrorState, EmptyState and
 * ForbiddenState declare `actionLabel` and render it as `{{ actionLabel }}`
 * (ErrorState.vue:32, EmptyState.vue:26, ForbiddenState.vue:26). Every static
 * `action-label="…"` in this repo (12 of them) sits on one of those three components, so
 * the name is rendered copy and belongs on this list. A value that no component renders
 * must NOT be added here — the list is an assertion about the render path, not a wish.
 */
const RENDERED_ATTRIBUTES = ['aria-label', 'placeholder', 'title', 'alt', 'label', 'message', 'description', 'action-label'];

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
  const template = /<template[^>]*>([\s\S]*)<\/template[^>]*>/i.exec(text)?.[1];
  const script = /<script[^>]*>([\s\S]*?)<\/script[^>]*>/i.exec(text)?.[1];
  // A plain .ts module has neither tag: treat the whole file as script, otherwise the
  // helper modules that render user copy (utils/*.ts) are invisible to this detector.
  if (template === undefined && script === undefined) return { template: '', script: text };
  return { template: template ?? '', script: script ?? '' };
}

/**
 * Literal text shown by the template. Interpolations are removed rather than used to
 * exclude the whole node: `<p>Sign out {{ name }}</p>` hides real copy, and the first
 * version of this detector skipped any node containing braces.
 *
 * A node is reported when it carries two consecutive letters OR a single Han character
 * (HAN_FRAGMENT): nodes render by construction, so the two-letter floor only exists to keep
 * one-letter ASCII noise out, and it also hid `共 个`, `与`, `个`, `条` and `（ 条）` — real
 * fragments the user reads (TASK-318).
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
    // Text nodes render by construction: `\p{L}{2,}` keeps the English/identifier floor,
    // and a single Han character is copy here (`共 {{ n }} 个` collapses to `共 个`, whose
    // two Han characters are not adjacent, and `与`/`个`/`条` stand alone).
    if (literal && (/\p{L}{2,}/u.test(literal) || HAN_FRAGMENT.test(literal))) literals.push(literal);
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
 *
 * The shape rule is English-shaped (capital, sentence punctuation or a space), so Chinese
 * copy such as `action-label="重试"` passed it even once the name was known: TASK-318
 * measured 12 such values, all on rendered components. The Han rule below closes the class
 * rather than the name, so a future alias prop cannot reopen it — and it cannot over-report
 * on this repo, because the same measurement shows every Han-carrying value in a
 * non-technical attribute is user-visible.
 */
export function attributeLiterals(text: string): string[] {
  const template = stripComments(sections(text).template);
  const literals: string[] = [];
  const looksLikeCopy = (value: string): boolean =>
    /\p{Lu}/u.test(value) || /[.!?。！？]$/u.test(value) || value.trim().includes(' ');
  const isCopyValue = (name: string, value: string): boolean => {
    const known = RENDERED_ATTRIBUTES.includes(name);
    const technical = NON_COPY_ATTRIBUTES.has(name) || name.startsWith('data-');
    return !technical && value.trim() !== '' && (known || looksLikeCopy(value) || isHanCopyLiteral(value));
  };
  // Only plain attributes: `:prop="expr"`, `@event="handler"` and `v-if` are expressions,
  // and their values are covered by templateExpressionLiterals instead.
  for (const match of template.matchAll(/\s((?!v-)[a-zA-Z][\w-]*)\s*=\s*"([^"{][^"]*)"/g)) {
    const [, name, value] = match as unknown as [string, string, string];
    if (isCopyValue(name, value)) literals.push(value.trim());
  }
  // Vue accepts single quotes around an attribute value too (`title='中文'`), and unlike the
  // double-quoted form that quote is ALSO the string syntax inside an expression, so the scan
  // runs on a copy with every double-quoted value and interpolation removed first: otherwise
  // an assignment inside a handler (`@click="mode = 'edit'"`) reads as an attribute named
  // `mode`. This repo uses double quotes exclusively (measured, TASK-318), so the branch
  // reports nothing today — it is here so the surface is complete rather than half-covered.
  const singleQuoted = template
    .replace(/\s[\w:@.-]+\s*=\s*"[^"]*"/g, ' ')
    .replace(/\{\{[\s\S]*?\}\}/g, ' ');
  for (const match of singleQuoted.matchAll(/\s((?!v-)[a-zA-Z][\w-]*)\s*=\s*'([^'{][^']*)'/g)) {
    const [, name, value] = match as unknown as [string, string, string];
    if (isCopyValue(name, value)) literals.push(value.trim());
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
    if (isSentenceLiteral(literal)) sentences.push(literal);
  }
  // Template literals are just as renderable; single words are too noisy to flag.
  for (const match of script.matchAll(/`([^`\\]*)`/g)) {
    const literal = match[1]!.trim();
    if (isSentenceLiteral(literal)) sentences.push(literal);
  }
  // Chinese copy carries no whitespace, so the sentence rule cannot see it, and a label
  // like '已创建定义' sat in a store while the gate stayed green. On this project's single
  // locale a Han run IS copy, so the same literals are scanned again with the Han rule;
  // the sentence rule's hits are skipped so nothing is reported twice, and regex literals
  // are blanked so a pattern like /'中文'/ cannot look like a string.
  const withoutRegex = blankRegexLiterals(script);
  for (const match of withoutRegex.matchAll(/'([^'\\\n]*)'|"([^"\\\n]*)"/g)) {
    const literal = (match[1] ?? match[2] ?? '').trim();
    if (isIdentifier(literal) || isSentenceLiteral(literal)) continue;
    if (isHanCopyLiteral(literal)) sentences.push(literal);
  }
  for (const match of withoutRegex.matchAll(/`([^`\\]*)`/g)) {
    const literal = match[1]!.trim();
    if (isSentenceLiteral(literal)) continue;
    if (isHanCopyLiteral(literal)) sentences.push(literal);
  }
  return sentences;
}

/**
 * A quoted script literal that reads as an English sentence: whitespace plus at least two
 * letter runs. Kept as a predicate of its own so the Chinese rule below can defer to it
 * instead of reporting the same literal twice.
 */
function isSentenceLiteral(literal: string): boolean {
  return literal.includes(' ') && (literal.match(/[\p{L}]{2,}/gu)?.length ?? 0) >= 2;
}

/**
 * Two or more consecutive Han characters. The console's locale is Chinese, so a run this
 * long inside a string literal is copy or a rendered label; identifiers, route names, CSS
 * classes, wire enums, reason codes and log/error codes are ASCII by construction, so the
 * rule cannot reach them. One Han character is deliberately not enough: separator
 * fragments such as '：' are structural, not copy (the same reason the English rule flags
 * sentences rather than single letters).
 */
const HAN_COPY = /\p{Script=Han}{2,}/u;

/**
 * A single Han character, for the surfaces that render by construction.
 *
 * `HAN_COPY` needs two because a code literal can be a separator (`changed.join('、')`);
 * a template TEXT node and a backtick template literal in a template expression are not
 * code — Vue renders them, so one Han character there is copy. TASK-318 measured the whole
 * repo: adopting this rule adds exactly six literals and all six are user-visible
 * quantifier/conjunction/unit fragments (`共 个`, `与`, `个`, `条`, `（ 条）`,
 * `${patch.length} 项`), so the narrower threshold is not a blanket relaxation. It is
 * deliberately NOT applied to quoted literals or to `<script>` literals; see
 * templateExpressionLiterals for the residual that keeps.
 */
const HAN_FRAGMENT = /\p{Script=Han}/u;

/**
 * Han-carrying literals that are not copy. Every class is matched as a SHAPE, so a real
 * label cannot hide behind one: a URL has a scheme, a test id appears as an
 * attribute-match expression, and a module specifier starts with a path prefix. The
 * remaining classes the audit named are ASCII by construction (CSS class lists,
 * `data-*` names, wire reason codes, log/error codes), which is why the Han run itself
 * is the rule that excludes them.
 */
function isMachineLiteral(literal: string): boolean {
  return (
    /^[a-z][a-z0-9+.-]*:\/\//i.test(literal) ||
    /data-(?:testid|test|qa)\s*=/.test(literal) ||
    /^(?:\.{0,2}\/|@\/)/.test(literal)
  );
}

function isHanCopyLiteral(literal: string): boolean {
  return HAN_COPY.test(literal) && !isMachineLiteral(literal);
}

/** The same exclusion, at one Han character, for the surfaces that render (see HAN_FRAGMENT). */
function isHanFragmentCopy(literal: string): boolean {
  return HAN_FRAGMENT.test(literal) && !isMachineLiteral(literal);
}

/**
 * Blanks regex literals so their bodies cannot be read as string literals.
 *
 * A regex shares the quote syntax with copy (`/'中文'/`), which is why a regex literal is
 * one of the exclusion classes. Quoted strings are copied through verbatim — their
 * contents, including the `//` inside `'https://…'`, never reach the `/` heuristic — and
 * `/` only opens a regex where the preceding token expects an expression, the same
 * disambiguation a lexer performs, so a division (`total / count`) is left untouched.
 *
 * Template literals are deliberately NOT consumed here: their quotes are handled by the
 * template scan of scriptSentences, and a raw backtick in this module would pair with the
 * backticks of that scan and make the detector read a block of its own code as copy.
 */
function blankRegexLiterals(script: string): string {
  const EXPRESSION_BEFORE = new Set([
    '(', '[', '{', ',', ';', ':', '=', '!', '&', '|', '?', '+', '-', '*', '%', '^', '~', '<', '>',
  ]);
  const KEYWORDS_BEFORE = new Set([
    'return', 'typeof', 'instanceof', 'in', 'of', 'new', 'delete', 'void', 'case', 'do', 'else',
    'yield', 'await',
  ]);
  // `if (flag) /re/.test(v)` really is a regex after a `)`, while `f(x) / 2` divides. The
  // two are told apart by the `(` the `)` closes: only a condition paren — the word before
  // it is a control keyword — may be followed by a regex literal. Without this the paren
  // case stayed unblanked and the review probe `if (flag) /'过报探针'/u.test(v)` was
  // reported as copy.
  const CONTROL_KEYWORDS = new Set(['if', 'while', 'for', 'switch', 'catch']);
  let blanked = '';
  let index = 0;
  let previous = '';
  let word = '';
  const parens: boolean[] = [];
  let regexAfterCondition = false;
  while (index < script.length) {
    const char = script[index]!;
    if (char === "'" || char === '"') {
      let cursor = index + 1;
      while (cursor < script.length) {
        if (script[cursor] === '\n') break;
        if (script[cursor] === '\\') {
          cursor += 2;
          continue;
        }
        if (script[cursor] === char) {
          cursor += 1;
          break;
        }
        cursor += 1;
      }
      blanked += script.slice(index, cursor);
      previous = char;
      word = '';
      regexAfterCondition = false;
      index = cursor;
      continue;
    }
    if (char === '(') {
      parens.push(CONTROL_KEYWORDS.has(word));
      blanked += char;
      previous = char;
      word = '';
      regexAfterCondition = false;
      index += 1;
      continue;
    }
    if (char === ')') {
      regexAfterCondition = parens.pop() ?? false;
      blanked += char;
      previous = char;
      word = '';
      index += 1;
      continue;
    }
    if (
      char === '/' &&
      (previous === '' || EXPRESSION_BEFORE.has(previous) || KEYWORDS_BEFORE.has(word) || regexAfterCondition)
    ) {
      let cursor = index + 1;
      let closed = false;
      // A `/` inside a character class does not end the literal: `/[/]'中文'/u` is a single
      // regex, and stopping at the class slash left its tail readable as a string literal
      // (the second over-report the review found).
      let inClass = false;
      while (cursor < script.length && script[cursor] !== '\n') {
        const peek = script[cursor]!;
        if (peek === '\\') {
          cursor += 2;
          continue;
        }
        if (peek === '[') {
          inClass = true;
        } else if (peek === ']') {
          inClass = false;
        } else if (peek === '/' && !inClass) {
          closed = true;
          break;
        }
        cursor += 1;
      }
      if (closed) {
        let end = cursor + 1;
        while (end < script.length && /[a-z]/i.test(script[end]!)) end += 1;
        blanked += ' '.repeat(end - index);
        previous = '/';
        word = '';
        regexAfterCondition = false;
        index = end;
        continue;
      }
    }
    blanked += char;
    if (/\s/.test(char)) {
      // Whitespace ends neither an expression nor a keyword run, and it may sit between
      // the `)` of a condition and the regex literal that follows it.
    } else if (/[\w$]/.test(char)) {
      previous = char;
      word += char;
      regexAfterCondition = false;
    } else {
      previous = char;
      word = '';
      regexAfterCondition = false;
    }
    index += 1;
  }
  return blanked;
}

/**
 * The expression regions of a template: `{{ … }}` interpolations and the bound directives
 * (`:prop="…"`, `@event="…"`, `v-if="…"`) whose values Vue evaluates as JavaScript.
 *
 * The Han scan below runs on these regions rather than on the raw template because the
 * template also contains markup: a tag close (`</span>`) puts a `/` where the expression
 * lexer expects an operand, so blanking regex literals over the raw template would read
 * `span>…</` as a regex body and swallow the copy between two tags.
 *
 * `data-*` values are machine identifiers (`:data-testid="…"`), not copy — the same
 * exclusion attributeLiterals applies to the plain attribute of that name.
 */
function templateExpressions(template: string): string[] {
  const expressions: string[] = [];
  for (const match of template.matchAll(/\{\{([\s\S]*?)\}\}/g)) expressions.push(match[1]!);
  for (const match of template.matchAll(/\s((?::|@|v-)[\w:.-]+)\s*=\s*"([^"]*)"/g)) {
    const name = match[1]!;
    if (name.replace(/^(?::|@|v-[\w-]+:)/, '').startsWith('data-')) continue;
    expressions.push(match[2]!);
  }
  return expressions;
}

/**
 * A quoted template expression that reads as English copy: a capitalised word or sentence
 * punctuation, at least two letters, and not an identifier. A class list
 * (`status status--active`) is not copy, while a capitalised single word ("Active") is.
 */
function isTemplateExpressionCopy(literal: string): boolean {
  return (
    (/\p{Lu}/u.test(literal) || /[.!?。！？]$/u.test(literal)) && /\p{L}{2,}/u.test(literal) && !isIdentifier(literal)
  );
}

/**
 * String literals inside the template — `{{ cond ? 'A' : 'B' }}` and `:title="'A'"`.
 * These render like any other copy but are invisible to a text-node scan, which is how
 * `Tenant boundary for release management.` slipped through. English is judged by shape,
 * Chinese by the Han run (see below).
 */
export function templateExpressionLiterals(text: string): string[] {
  // Identifiers live in the same expression syntax as copy, so strip them first:
  // catalog lookups t('key'), vue-router route names (name: 'Home'), and params.
  const template = stripIdentifierPositions(stripComments(sections(text).template));
  const literals: string[] = [];
  for (const match of template.matchAll(/'([^'\n]+)'/g)) {
    const literal = match[1]!.trim();
    if (isTemplateExpressionCopy(literal)) literals.push(literal);
  }
  // Chinese copy has neither a capital letter nor a space, so the shape rule above cannot
  // see `{{ preparing ? '准备中…' : '完成' }}`: it rendered Chinese while the gate stayed
  // green. TASK-310 gave the script layer a Han rule for exactly this reason; this is its
  // template-expression counterpart, and it reuses that rule's predicates
  // (HAN_COPY / isMachineLiteral) and its regex blanking instead of growing a second
  // detector. Literals the shape rule already reported are skipped, so no literal is
  // reported twice.
  for (const expression of templateExpressions(template)) {
    const withoutRegex = blankRegexLiterals(expression);
    for (const match of withoutRegex.matchAll(/'([^'\n]+)'|"([^"\n]+)"/g)) {
      const literal = (match[1] ?? match[2]!).trim();
      if (isTemplateExpressionCopy(literal)) continue;
      if (isHanCopyLiteral(literal)) literals.push(literal);
    }
    // Backtick template literals are scanned by the Han rule only. The shape rule cannot
    // judge them here: a class list or a test id that interpolates camelCase
    // (`operation-row-${row.operationId}`) carries a capital and reads exactly like
    // `Rollback ${releaseName}`, so it would report 29 machine values to catch 6 labels.
    // A run of Han characters does not occur in those machine values at all, which is the
    // same reason the script layer's Han pass can scan backticks unambiguously.
    //
    // A backtick literal is a composed, rendered string, so ONE Han character is enough:
    // `{{ n > 0 ? `${n} 项` : t('operation.confirm.noPatch') }}` rendered `项` while the
    // gate stayed green (TASK-318). The quoted form keeps HAN_COPY: a lone Han quoted
    // literal is indistinguishable at this level from a code-level separator, and the repo
    // has none (measured, TASK-318) — that residual is pinned by the '无' case below.
    for (const match of withoutRegex.matchAll(/`([^`]+)`/g)) {
      const literal = match[1]!.trim();
      if (isHanFragmentCopy(literal)) literals.push(literal);
    }
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
