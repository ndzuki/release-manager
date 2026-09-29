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
