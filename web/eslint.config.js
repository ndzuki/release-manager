import js from '@eslint/js';
import eslintConfigPrettier from 'eslint-config-prettier';
import globals from 'globals';
import tseslint from 'typescript-eslint';
import vue from 'eslint-plugin-vue';
import vueA11y from 'eslint-plugin-vuejs-accessibility';

/*
 * Accessibility gate (TASK-267). This is a *deliberate subset*, not the plugin's
 * `recommended` preset: every enabled rule is listed with its measured violation
 * count so the next person can see why it is here. Adding a rule back is a data
 * question — run it over `src` first.
 *
 * Enabled (19):
 *   - 18 rules measured at 0 violations across src (pure regression guards):
 *     alt-text, anchor-has-content, aria-props, aria-role,
 *     aria-unsupported-elements, form-control-has-label, heading-has-content,
 *     iframe-has-title, interactive-supports-focus, media-has-caption,
 *     mouse-events-have-key-events, no-access-key, no-aria-hidden-on-focusable,
 *     no-autofocus, no-distracting-elements, no-role-presentation-on-focusable,
 *     role-has-required-aria-props, tabindex-no-positive.
 *   - label-has-for, configured to the WCAG 1.3.1 / 3.3.2 requirement: a label
 *     must associate by nesting *or* by `for`. The plugin's default demands both
 *     (`required: { every: ['nesting', 'id'] }`), which flagged all 87 labels that
 *     associate by nesting — valid HTML and valid AT association.
 *
 * Deliberately NOT enabled (measured, see web/README.md "Accessibility gate"):
 *   - no-redundant-roles (21): all in ReleaseInventoryTable.vue, where the card
 *     layout below 40rem sets `display: block/grid` and the explicit roles are
 *     kept as a cross-engine safety net. Measured on 2026-10-18: Chromium keeps
 *     table/row/cell without them, so the rule is right for this engine;
 *     Firefox/WebKit were not tested, so removing the roles is unverified.
 *   - no-static-element-interactions (2) and click-events-have-key-events (1):
 *     AppDialog's backdrop (`@click.self`, keyboard close lives in useFocusTrap)
 *     and Toast's hover-pause on a `role="status"` element.
 *   - no-onchange (13): its premise (a `change` handler fires per keystroke) is
 *     React-specific; Vue's `@change` fires on commit.
 */
const a11yRules = {
  'vuejs-accessibility/alt-text': 'error',
  'vuejs-accessibility/anchor-has-content': 'error',
  'vuejs-accessibility/aria-props': 'error',
  'vuejs-accessibility/aria-role': 'error',
  'vuejs-accessibility/aria-unsupported-elements': 'error',
  'vuejs-accessibility/form-control-has-label': 'error',
  'vuejs-accessibility/heading-has-content': 'error',
  'vuejs-accessibility/iframe-has-title': 'error',
  'vuejs-accessibility/interactive-supports-focus': 'error',
  'vuejs-accessibility/label-has-for': ['error', { required: { some: ['nesting', 'id'] } }],
  'vuejs-accessibility/media-has-caption': 'error',
  'vuejs-accessibility/mouse-events-have-key-events': 'error',
  'vuejs-accessibility/no-access-key': 'error',
  'vuejs-accessibility/no-aria-hidden-on-focusable': 'error',
  'vuejs-accessibility/no-autofocus': 'error',
  'vuejs-accessibility/no-distracting-elements': 'error',
  'vuejs-accessibility/no-role-presentation-on-focusable': 'error',
  'vuejs-accessibility/role-has-required-aria-props': 'error',
  'vuejs-accessibility/tabindex-no-positive': 'error',
};

export default tseslint.config(
  { ignores: ['dist/**', 'src/gen/**', '*.d.ts', '*.d.ts.map', '*.tsbuildinfo'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  ...vue.configs['flat/recommended'],
  eslintConfigPrettier,
  {
    files: ['**/*.{ts,vue}'],
    languageOptions: {
      globals: globals.browser,
      parserOptions: {
        parser: tseslint.parser,
        extraFileExtensions: ['.vue'],
      },
    },
    rules: {
      'vue/multi-word-component-names': 'off',
      '@typescript-eslint/no-explicit-any': 'error',
    },
  },
  {
    files: ['**/*.vue'],
    plugins: { 'vuejs-accessibility': vueA11y },
    rules: a11yRules,
  },
  {
    files: ['**/*.test.ts'],
    languageOptions: {
      globals: { ...globals.browser, ...globals.node },
    },
  },
);
