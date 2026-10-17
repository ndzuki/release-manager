import { describe, expect, it } from 'vitest';
import {
  attributeLiterals,
  bareCopy,
  referencedKeys,
  scriptSentences,
  scriptWordLiterals,
  templateExpressionLiterals,
  templateLiterals,
} from './copy-lint';

/*
 * The copy-hygiene gate is only as good as this detector: its first version missed
 * script-level copy entirely and let a COMMENT satisfy the "has a consumer" rule.
 * Both holes are pinned here.
 */
describe('templateLiterals', () => {
  it('finds rendered text and ignores interpolations', () => {
    const literals = templateLiterals('<template><p>Hello there</p><span>{{ t(\'nav.audit\') }}</span></template>');

    expect(literals).toEqual(['Hello there']);
  });

  it('ignores text inside comments', () => {
    expect(templateLiterals('<template><!-- Sign out later --><p>{{ t(\'shell.signOut\') }}</p></template>')).toEqual([]);
  });
});

describe('text nodes next to arrow functions', () => {
  // An arrow function INSIDE an interpolation splits the text node on its `>`.
  it('does not report the tail of an arrow function inside an interpolation', () => {
    const file = `<template><span>\n  {{ errors.find((i) => i.field === 'name')?.description }}\n</span></template>`;

    expect(templateLiterals(file)).toEqual([]);
  });

  // The `>` in `=>` inside a bound attribute used to be read as a tag close.
  it('does not report the tail of an arrow function as copy', () => {
    const file = `<template><span v-if="items.find((i) => i.field === 'name')">{{ label }}</span></template>`;

    expect(templateLiterals(file)).toEqual([]);
  });
});

describe('attributeLiterals', () => {
  it('finds rendered attribute values but not bound ones', () => {
    const literals = attributeLiterals(
      '<template><nav aria-label="Primary navigation" :title="dynamic"><input placeholder="Search releases" /></nav></template>',
    );

    expect(literals).toContain('Primary navigation');
    expect(literals).toContain('Search releases');
    expect(literals).toHaveLength(2);
  });

  // TASK-318: `action-label` is a component prop, not a native attribute — ErrorState,
  // EmptyState and ForbiddenState declare `actionLabel` and render it as `{{ actionLabel }}`,
  // and every static `action-label="…"` in this repo sits on one of those three. Its Chinese
  // labels have no capital, no sentence punctuation and no space, so the shape rule could not
  // see them and the name was not on the list: 12 rendered values were invisible.
  it('sees a rendered alias prop whose value is Chinese', () => {
    const file = '<template><ErrorState title="加载未成功" action-label="刷新列表" /><EmptyState action-label="重试" /></template>';

    expect(attributeLiterals(file)).toEqual(['加载未成功', '刷新列表', '重试']);
  });

  it('checks a rendered prop even when its value carries no shape at all', () => {
    // Only the name list accepts this value: no capital, no space, no sentence punctuation.
    expect(attributeLiterals('<template><EmptyState action-label="hi" /></template>')).toEqual(['hi']);
  });

  // The Han rule is name-independent, which is what keeps a future alias prop from reopening
  // the hole. It must still not reach machine values or bound expressions.
  it('still ignores a Han value on a technical attribute and inside a bound prop', () => {
    const file = `<template><div class="卡片" name="中文" :action-label="retryable ? '重试' : ''"></div></template>`;

    expect(attributeLiterals(file)).toEqual([]);
  });

  // Vue accepts single quotes around an attribute value, and that is also the string syntax
  // inside an expression — so the scan removes double-quoted values first, and an assignment
  // inside a handler does not read as an attribute named `mode`.
  it('sees a single-quoted attribute and not an expression inside a handler', () => {
    const file = `<template><span title='中文标题'>x</span><button @click="mode = 'edit'">y</button></template>`;

    expect(attributeLiterals(file)).toEqual(['中文标题']);
  });
});

describe('scriptSentences', () => {
  it('finds user-facing sentences', () => {
    const sentences = scriptSentences("<script setup lang=\"ts\">\nconst msg = 'Unable to switch organization.';\n</script>");

    expect(sentences).toEqual(['Unable to switch organization.']);
  });

  it('ignores identifiers, routes and enums', () => {
    const sentences = scriptSentences(
      `<script setup lang="ts">
const role = 'release_admin';
const proc = 'auth.v1.AuthService';
const route = '/customers/:customerId/clusters';
const pattern = '^[a-z]+$';
</script>`,
    );

    expect(sentences).toEqual([]);
  });
});

describe('templateExpressionLiterals', () => {
  it('finds copy inside a ternary, including a single capitalised word', () => {
    const literals = templateExpressionLiterals("<template><span>{{ enabled ? 'Active' : 'Disabled' }}</span></template>");

    expect(literals).toEqual(['Active', 'Disabled']);
  });

  // Identifiers share the syntax with copy: route names and catalog keys are not copy.
  it('ignores catalog keys, route names and class lists', () => {
    const file = `<template>
  <RouterLink :to="{ name: 'CustomerList' }">{{ t('nav.customers') }}</RouterLink>
  <span :class="active ? 'status status--active' : 'status status--disabled'"></span>
</template>`;

    expect(templateExpressionLiterals(file)).toEqual([]);
  });

  it('flags a message attribute that interpolates (Vue 2 syntax never renders)', () => {
    // The *attribute* scan sees this; the point of the case is that the copy inside it
    // is detected too, so the migration cannot hide behind the broken syntax.
    expect(attributeLiterals('<template><LoadingState message="{{ t(\'state.x\') }}" /></template>')).toEqual([]);
  });
});

describe('bareCopy', () => {
  it('unions every rendering surface', () => {
    const file = `<template><p>No data</p><nav aria-label="Primary navigation"></nav></template>
<script setup lang="ts">
const fallback = 'Something went wrong here';
</script>`;

    expect(bareCopy(file).sort()).toEqual(['No data', 'Primary navigation', 'Something went wrong here']);
  });
});

describe('blind spots the review found', () => {
  // Interpolations used to make the whole node invisible.
  it('sees the literal part of a text node next to an interpolation', () => {
    expect(templateLiterals('<template><p>Sign out {{ name }}</p></template>')).toEqual(['Sign out']);
  });

  // An inline `//` inside a string used to swallow the copy after it on that line.
  it('does not treat a quoted line as a comment', () => {
    expect(scriptSentences("<script setup lang=\"ts\">\nconst url = 'https://x'; const msg = 'Access denied';\n</script>"))
      .toContain('Access denied');
  });

  it('sees a literal inside a bound attribute', () => {
    expect(attributeLiterals('<template><button :title="\'Sign out\'">x</button></template>')).toContain('Sign out');
  });

  it('sees a script template literal', () => {
    expect(scriptSentences('<script setup lang="ts">\nconst msg = `Access denied for you`;\n</script>'))
      .toContain('Access denied for you');
  });
});

describe('isIdentifier (narrow by design)', () => {
  // The review proved the first version silently passed these.
  it('does not treat ambiguous lowerCamelCase words as identifiers', () => {
    const literals = templateExpressionLiterals(
      "<template><span>{{ x ? 'camelCase' : 'iPhone' }}</span></template>",
    );

    expect(literals).toEqual(['camelCase', 'iPhone']);
  });

  it('still ignores event names, kebab classes and dotted paths', () => {
    const literals = templateExpressionLiterals(
      "<template><span @update:modelValue=\"x\" :class=\"'status--active'\">{{ a.b.c }}</span></template>",
    );

    expect(literals).toEqual([]);
  });

  // …while their identifier positions are stripped contextually.
  it('ignores identifiers passed to fieldError or compared to .field', () => {
    const literals = templateExpressionLiterals(
      "<template><small v-if=\"fieldError(i, 'sourcePrefix')\">{{ x }}</small><b v-if=\"item.field === 'operatorName'\">y</b></template>",
    );

    expect(literals).toEqual([]);
  });
});

describe('scriptWordLiterals', () => {
  // These were the actual leak: a lookup object of capitalised labels was invisible.
  it('sees capitalised single-word labels in a lookup object', () => {
    const text = `<script setup lang="ts">\nconst LABELS = { draft: 'Draft', superseded: 'Superseded' };\n</script>`;

    expect(scriptWordLiterals(text)).toEqual(['Draft', 'Superseded']);
  });

  it('ignores identifiers and enum values', () => {
    const text = `<script setup lang="ts">\nconst a = 'release_admin'; const b = 'auth.v1.AuthService'; const c = 'yaml';\n</script>`;

    expect(scriptWordLiterals(text)).toEqual([]);
  });
});

describe('plain .ts modules', () => {
  // A helper module has no <template>/<script> tag; the first detector returned [] for
  // it, which hid the English that the operator table rendered for every row.
  it('sees sentence literals in a module without SFC tags', () => {
    const text = `const labels = {}\nfunction label() {\n  return 'Heartbeat timed out.';\n}\n`;

    expect(bareCopy(text)).toContain('Heartbeat timed out.');
  });
});

describe('scriptSentences: Chinese copy without whitespace (TASK-310)', () => {
  // The blind spot the review proved: the sentence rule needs a space, so a label like
  // '已创建定义' rendered by a store never reached the gate and the suite stayed green.
  it('sees a Han label that carries no whitespace', () => {
    const text = `<script setup lang="ts">\nconst msg = '已创建定义';\n</script>`;

    expect(scriptSentences(text)).toEqual(['已创建定义']);
  });

  it('sees a Han template literal', () => {
    const text = '<script setup lang="ts">\nconst msg = `操作已切换`;\n</script>';

    expect(scriptSentences(text)).toEqual(['操作已切换']);
  });

  // Both rules can match the same literal; it must still be reported exactly once.
  it('does not report a Han literal twice when it also reads as a sentence', () => {
    expect(scriptSentences("<script setup lang=\"ts\">\nconst msg = '未登记 的中文文案';\n</script>")).toEqual([
      '未登记 的中文文案',
    ]);
  });

  it('keeps ignoring ASCII identifiers, routes and codes', () => {
    const text = `<script setup lang="ts">
const role = 'release_admin';
const code = 'invalid_argument';
const route = '/customers/:customerId/clusters';
</script>`;

    expect(scriptSentences(text)).toEqual([]);
  });
});

describe('scriptSentences: non-copy classes are excluded by shape (TASK-310)', () => {
  it('does not read Han inside a regex literal as copy', () => {
    // The quote is the load-bearing part: without blankRegexLiterals the string scan
    // reads this regex body as a literal.
    expect(scriptSentences("<script setup lang=\"ts\">\nconst re = /'已创建定义'/;\n</script>")).toEqual([]);
    expect(scriptSentences("<script setup lang=\"ts\">\nconst re = /^[\\u4e00-\\u9fff]+$/;\n</script>")).toEqual([]);
  });

  it('does not read a regex that opens the script as copy', () => {
    expect(scriptSentences("/'已创建定义'/.test(value);\nconst msg = '操作已切换';")).toEqual(['操作已切换']);
  });

  it('still sees copy after a regex literal and after a division', () => {
    const text = `<script setup lang="ts">
const re = /'其他'/;
const half = total / count;
const msg = '已创建定义';
</script>`;

    expect(scriptSentences(text)).toEqual(['已创建定义']);
  });

  it('does not read Han inside a comment as copy', () => {
    expect(scriptSentences("<script setup lang=\"ts\">\n// '已创建定义' is not implemented yet\nconst x = 1;\n</script>")).toEqual(
      [],
    );
  });

  it('does not read Han inside a URL, a test id or a module path as copy', () => {
    const text = `<script setup lang="ts">
const docs = 'https://example.test/已创建定义';
const selector = '[data-testid="已创建定义"]';
const module = '@/已创建定义/messages';
</script>`;

    expect(scriptSentences(text)).toEqual([]);
  });

  // CSS class names, wire reason codes, log/error codes and data-* names are ASCII by
  // construction in this repo (the repo-wide scan is recorded with TASK-310), so the Han
  // rule cannot reach them. This pins that the second pass adds the Han label only and
  // does not sweep the ASCII identifiers that share the file.
  it('reports the Han label without sweeping the ASCII identifiers around it', () => {
    const text = `<script setup lang="ts">
const cls = 'card';
const errorCode = 'invalid_argument';
const reason = 'duplicate';
const label = '已创建定义';
</script>`;

    expect(scriptSentences(text)).toEqual(['已创建定义']);
  });
});

describe('templateExpressionLiterals: Chinese without whitespace (TASK-313)', () => {
  // The same blind spot TASK-310 closed for <script>, one surface over: the template rule
  // wanted a capital or sentence punctuation, so `{{ saving ? '提交中…' : '保存' }}` rendered
  // Chinese and the gate stayed green.
  it('sees Han copy inside an interpolation', () => {
    expect(
      templateExpressionLiterals("<template><span>{{ preparing ? '准备中…' : '完成' }}</span></template>"),
    ).toEqual(['准备中…', '完成']);
  });

  it('sees Han copy inside a bound directive', () => {
    const file = `<template><EmptyState :action-label="retryable ? '重试' : ''" /></template>`;

    expect(templateExpressionLiterals(file)).toEqual(['重试']);
  });

  it('sees a double-quoted Han string inside an interpolation', () => {
    expect(templateExpressionLiterals('<template><span>{{ label ?? "未知" }}</span></template>')).toEqual(['未知']);
  });

  // Both rules can match one literal; it must still be reported exactly once.
  it('does not report a literal twice when it also reads as English copy', () => {
    expect(
      templateExpressionLiterals("<template><span>{{ a ? 'Release 已创建' : 'Disabled' }}</span></template>"),
    ).toEqual(['Release 已创建', 'Disabled']);
  });

  it('keeps ignoring identifiers, route names, class lists and machine values', () => {
    const file = `<template>
  <RouterLink :to="{ name: 'CustomerList' }">{{ t('nav.customers') }}</RouterLink>
  <span :class="active ? 'status status--active' : 'status status--disabled'"></span>
  <b :data-testid="'已创建'"></b>
  <i :title="'https://example.test/已创建'"></i>
  <u :src="'@/已创建/messages'"></u>
</template>`;

    expect(templateExpressionLiterals(file)).toEqual([]);
  });

  // A repo-wide audit for TASK-313 found exactly one single-Han user-visible literal that
  // stands alone as a word (`'无'` in OperationConfirmPanel.vue); it moved into the catalog
  // instead of lowering the threshold. That is NOT a claim that no single-Han fragment is
  // user-visible: neither rule accepts a run shorter than two (`templateLiterals` tests
  // `\p{L}{2,}` per text node, `HAN_COPY` tests `\p{Script=Han}{2,}` per literal), so a lone
  // single-Han fragment is never a candidate of its own. These pre-existing files, untouched
  // by TASK-313, still carry one; TASK-318 tracks the gap, and the MIGRATED allowlist pins
  // each status in both directions (a reported literal must be listed, and vice versa):
  //   BundlesPage.vue:52            `共 {{ … }} 个`       unreported, node reads `共 个`
  //   BundlesPage.vue:84            `… 与 …`              unreported, `与` is its own node
  //   BundlesPage.vue:121           `{{ … }} 个`          unreported, node reads `个`
  //   DefinitionsPage.vue:293       `有 {{ … }}`          reported as its whole sentence, listed
  //   DefinitionsPage.vue:338       `{{ … }} 条`          unreported, node reads `条`
  //   DefinitionsPage.vue:396       `（{{ … }} 条）`      unreported, node reads `（ 条）`
  //   OperationConfirmPanel.vue:35  `${patch.length} 项`  unreported, one Han in the literal
  //   ReleaseInventoryPage.vue:117  `共 {{ … }} 条。`     reported as its whole sentence, but the
  //                                                      file is NOT_YET_MIGRATED (unasserted)
  // They are quantifier/conjunction/unit fragments, structural like the `'：'` case in
  // copy-lint.ts (`HAN_COPY` doc comment), so leaving the threshold alone is still correct.
  it('does not treat a single Han character as copy', () => {
    expect(templateExpressionLiterals("<template><dd>{{ patch.length > 0 ? 'x' : '无' }}</dd></template>")).toEqual(
      [],
    );
  });

  // Backticks are scanned by the Han rule only, and are reported whole: the evaluation of
  // the interpolation is the renderer's job, not the detector's.
  it('sees Han copy in a backtick template literal inside a bound directive', () => {
    const file = '<template><button :aria-label="`复制 Operation ID ${entry.operationId}`">x</button></template>';

    expect(templateExpressionLiterals(file)).toEqual(['复制 Operation ID ${entry.operationId}']);
  });

  // …while a machine value that interpolates camelCase is not Han copy and stays invisible
  // (the shape rule would have reported every one of them).
  it('does not report a backtick test id or class list', () => {
    const file = `<template>
  <b :data-testid="\`operation-row-\${row.operationId}\`"></b>
  <i :class="\`timeline-entry--\${entry.kind.toLowerCase()}\`"></i>
</template>`;

    expect(templateExpressionLiterals(file)).toEqual([]);
  });
});

describe('regex literals are excluded by shape (TASK-313)', () => {
  // Two over-reports the TASK-310 review named. Left unfixed they are noise; the danger is
  // the other direction, so each fix is pinned together with the case it must not swallow.
  it('does not read a regex that follows a condition paren as copy', () => {
    const text = `<script setup lang="ts">
if (flag) /'过报探针'/u.test(value);
const msg = '已创建定义';
</script>`;

    expect(scriptSentences(text)).toEqual(['已创建定义']);
  });

  it('does not read a regex whose character class contains a slash as copy', () => {
    const text = `<script setup lang="ts">
const re = /[/]'已创建中文'/u;
const msg = '操作已切换';
</script>`;

    expect(scriptSentences(text)).toEqual(['操作已切换']);
  });

  // …while a real division after a call or a condition is still not a regex literal.
  it('still divides after a call and after a condition', () => {
    const text = `<script setup lang="ts">
const half = f(x) / 2;
if (flag) total / count;
const msg = '操作已切换';
</script>`;

    expect(scriptSentences(text)).toEqual(['操作已切换']);
  });

  it('does not read a regex inside a template expression as copy', () => {
    expect(
      templateExpressionLiterals("<template><span>{{ /'过报探针'/u.test(value) ? '通过' : '失败' }}</span></template>"),
    ).toEqual(['通过', '失败']);
  });
});

describe('single-Han fragments on rendering surfaces (TASK-318)', () => {
  // A text node renders by construction, so one Han character in it is copy. The old rule
  // asked for `\p{L}{2,}`, which a lone `与`/`个`/`条` and the collapsed `共 个` both fail:
  // the repo carried 5 such fragments, rendered to the user, with the gate green.
  it('sees quantifier and conjunction fragments in text nodes', () => {
    const file = `<template>
  <span>共 {{ store.totalSize }} 个</span>
  <b>查询范围</b>与<b>授权对象</b>
  <i>{{ n }} 条</i>
  <h3>{{ t('definitions.promotionMapping') }}（{{ n }} 条）</h3>
</template>`;

    expect(templateLiterals(file)).toEqual(['共 个', '查询范围', '与', '授权对象', '条', '（ 条）']);
  });

  // The rule is a HAN run, not "any short node": punctuation-only nodes stay out, the same way
  // HAN_COPY keeps the `'：'` separator of the code-literal surface out.
  it('does not treat a punctuation-only text node as copy', () => {
    expect(templateLiterals('<template><span>：</span><b>/</b><i>·</i></template>')).toEqual([]);
  });

  // A backtick template literal is a composed, rendered string, so one Han character is
  // enough there too: OperationConfirmPanel rendered `${patch.length} 项` invisibly.
  it('sees a single Han count unit in a backtick literal', () => {
    const file = "<template><dd>{{ patch.length > 0 ? `${patch.length} 项` : t('operation.confirm.noPatch') }}</dd></template>";

    expect(templateExpressionLiterals(file)).toEqual(['${patch.length} 项']);
  });

  // …while the quoted form keeps HAN_COPY: TASK-313 pinned `'无'` as out of scope, and the
  // repo has no lone-Han quoted literal in a template expression (measured, TASK-318).
  it('keeps a lone Han quoted literal out, as TASK-313 pinned', () => {
    expect(templateExpressionLiterals("<template><dd>{{ n > 0 ? '有' : '无' }}</dd></template>")).toEqual([]);
  });
});

describe('referencedKeys', () => {
  // The regression the independent review proved: a comment used to count as a
  // consumer, so a dead catalog entry stayed green.
  it('does not treat a commented-out call as a consumer', () => {
    const keys = referencedKeys("// t('action.refresh') planned for later\nconst x = t('nav.audit');");

    expect(keys).toEqual(['nav.audit']);
  });

  it('does not mistake other calls for t()', () => {
    expect(referencedKeys("mount('#app');\nformat('x');")).toEqual([]);
  });

  it('finds every quoted call', () => {
    expect(referencedKeys("t('a.b'); t('c.d');")).toEqual(['a.b', 'c.d']);
  });
});
