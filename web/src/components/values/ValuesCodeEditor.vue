<script setup lang="ts">
import { t } from '@/i18n/messages';
import { onBeforeUnmount, onMounted, useTemplateRef, watch } from 'vue';
import { Compartment, EditorState } from '@codemirror/state';
import {
  HighlightStyle,
  bracketMatching,
  defaultHighlightStyle,
  indentOnInput,
  syntaxHighlighting,
} from '@codemirror/language';
import { json } from '@codemirror/lang-json';
import { yaml } from '@codemirror/lang-yaml';
import { lintGutter, linter, type Diagnostic } from '@codemirror/lint';
import {
  EditorView,
  highlightActiveLine,
  highlightActiveLineGutter,
  lineNumbers,
} from '@codemirror/view';
import { useTheme, type ResolvedTheme } from '@/composables/useTheme';
import type { EditorLanguage, ValidationIssue } from '@/types/valuesRevision';
import { validateValuesDocument } from '@/utils/valuesValidation';

const props = defineProps<{
  modelValue: string;
  language: EditorLanguage;
  readOnly?: boolean;
  serverIssue?: ValidationIssue | null;
}>();

const emit = defineEmits<{
  'update:modelValue': [value: string];
}>();

const host = useTemplateRef<HTMLDivElement>('host');
const languageCompartment = new Compartment();
const readOnlyCompartment = new Compartment();
const diagnosticsCompartment = new Compartment();
const themeCompartment = new Compartment();
const { resolvedTheme } = useTheme();
let view: EditorView | undefined;

type SyntaxTag = Parameters<typeof HighlightStyle.define>[0][number]['tag'];

/*
 * TASK-394: the tag instances come from CodeMirror's own default style instead of an
 * `@lezer/highlight` import. That package is only a TRANSITIVE dependency of
 * @codemirror/language (the console does not declare it, and this change must not add
 * dependencies), while HighlightStyle matches tags by object identity — the instances
 * must come from the very module the bundled JSON/YAML parsers used. `.set` is walked
 * because the tags that matter are usually expressed as a parent: a number is tagged
 * `literal`, a YAML key `definition(propertyName)`, a line comment `lineComment`.
 */
const syntaxTags = (() => {
  const byName = new Map<string, SyntaxTag>();
  for (const spec of defaultHighlightStyle.specs) {
    const tags = Array.isArray(spec.tag) ? spec.tag : [spec.tag];
    for (const tag of tags) {
      for (const member of tag.set) {
        const name = String(member);
        if (!byName.has(name)) byName.set(name, member);
      }
    }
  }
  return byName;
})();

function syntaxTag(name: string): SyntaxTag {
  const tag = syntaxTags.get(name);
  // Fails fast if a CodeMirror upgrade stops exposing one of the tags above: an
  // unhighlighted editor must not ship silently.
  if (!tag) throw new Error(`missing-syntax-tag:${name}`);
  return tag;
}

/*
 * The reported defect: the editor rendered CodeMirror's default (light) highlight style
 * on the dark `#0f172a` surface, so a YAML key was `#0000cc` on `#0f172a` = 1.59:1 and
 * the deployed build measured a 3.75:1 gutter. These accents are the direction-C palette
 * expressed as CSS variables, so ONE style resolves to the dark accents
 * (#7dd3fc/#fcd34d/#c4b5fd) or the light ones (#0369a1/#b45309/#6d28d9) with the theme.
 * Every pair is ≥4.5:1 on `--editor-surface`; tokens.test.ts computes the ratios from
 * tokens.css instead of trusting this comment.
 *
 * Mapping notes: `null` is a `keyword` subtag and punctuation is not part of CodeMirror's
 * default style, so JSON punctuation deliberately inherits the editor's text colour —
 * structural characters may be quiet, keys/strings/numbers may not.
 */
const syntaxStyle = HighlightStyle.define([
  { tag: syntaxTag('keyword'), color: 'var(--k)' },
  { tag: syntaxTag('propertyName'), color: 'var(--k)' },
  { tag: syntaxTag('labelName'), color: 'var(--k)' },
  { tag: syntaxTag('typeName'), color: 'var(--n)' },
  { tag: syntaxTag('string'), color: 'var(--s)' },
  { tag: syntaxTag('bool'), color: 'var(--n)' },
  { tag: syntaxTag('literal'), color: 'var(--n)' }, // numbers, and anything else literal
  { tag: syntaxTag('comment'), color: 'var(--color-muted-strong)', fontStyle: 'italic' },
  { tag: syntaxTag('meta'), color: 'var(--color-muted-strong)' },
  { tag: syntaxTag('invalid'), color: 'var(--er)' },
]);

/*
 * The surface, ink, caret, gutter and selection all alias the `--editor-*` tokens, which
 * resolve through the active palette. `dark` only tells CodeMirror which of ITS built-in
 * base styles (caret/selection/panel defaults) to key on, so it is passed from the
 * resolved theme and reconfigured when the shell toggles.
 */
const editorThemeSpec = {
  '&': { minHeight: '30rem', backgroundColor: 'var(--editor-surface)', color: 'var(--editor-text)' },
  '.cm-content': { minHeight: '30rem', padding: '1rem', caretColor: 'var(--editor-caret)', fontFamily: 'var(--font-family-mono)', fontSize: 'var(--font-size-md)' },
  '.cm-gutters': { backgroundColor: 'var(--editor-gutter-surface)', color: 'var(--editor-gutter-text)', border: 'none' },
  '.cm-activeLine': { backgroundColor: 'var(--editor-active-line)' },
  '.cm-activeLineGutter': { backgroundColor: 'var(--editor-active-line-gutter)' },
  '.cm-cursor': { borderLeftColor: 'var(--editor-caret)' },
  '.cm-dropCursor': { borderLeftColor: 'var(--editor-caret)' },
  '.cm-selectionBackground': { backgroundColor: 'var(--editor-selection)' },
  '.cm-scroller': { overflow: 'auto' },
};

function themeExtension(theme: ResolvedTheme) {
  return EditorView.theme(editorThemeSpec, { dark: theme === 'dark' });
}

function languageExtension(language: EditorLanguage) {
  return language === 'json' ? json() : yaml();
}

function diagnostics(state: EditorState): Diagnostic[] {
  const localIssue = validateValuesDocument(state.doc.toString()).issue;
  const issue = props.serverIssue ?? localIssue;
  if (!issue) return [];
  const lineNumber = Math.min(Math.max(issue.line ?? 1, 1), state.doc.lines);
  const line = state.doc.line(lineNumber);
  const from = issue.column ? Math.min(line.to, line.from + Math.max(issue.column - 1, 0)) : line.from;
  return [{
    from,
    to: Math.max(from, line.to),
    severity: issue.code === 'secret_literal_forbidden' ? 'warning' : 'error',
    message: issue.message,
    source: t('values.editor.validationAria'),
  }];
}

function diagnosticExtension() {
  return [linter((editor) => diagnostics(editor.state), { delay: 250 }), lintGutter()];
}

onMounted(() => {
  if (!host.value) return;
  view = new EditorView({
    parent: host.value,
    state: EditorState.create({
      doc: props.modelValue,
      extensions: [
        lineNumbers(),
        highlightActiveLineGutter(),
        highlightActiveLine(),
        bracketMatching(),
        indentOnInput(),
        syntaxHighlighting(syntaxStyle),
        themeCompartment.of(themeExtension(resolvedTheme.value)),
        languageCompartment.of(languageExtension(props.language)),
        readOnlyCompartment.of([EditorState.readOnly.of(Boolean(props.readOnly)), EditorView.editable.of(!props.readOnly)]),
        diagnosticsCompartment.of(diagnosticExtension()),
        EditorView.lineWrapping,
        EditorView.updateListener.of((update) => {
          if (update.docChanged) emit('update:modelValue', update.state.doc.toString());
        }),
      ],
    }),
  });
});

watch(() => props.modelValue, (value) => {
  if (!view || value === view.state.doc.toString()) return;
  view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value } });
});

watch(() => props.language, (language) => {
  view?.dispatch({ effects: languageCompartment.reconfigure(languageExtension(language)) });
});

watch(() => props.readOnly, (readOnly) => {
  view?.dispatch({ effects: readOnlyCompartment.reconfigure([EditorState.readOnly.of(Boolean(readOnly)), EditorView.editable.of(!readOnly)]) });
});

watch(() => props.serverIssue, () => {
  view?.dispatch({ effects: diagnosticsCompartment.reconfigure(diagnosticExtension()) });
});

// CodeMirror keys a few built-in base styles on `dark`, so the compartment carries the
// resolved theme; the colours themselves are CSS variables and follow on their own.
watch(resolvedTheme, (theme) => {
  view?.dispatch({ effects: themeCompartment.reconfigure(themeExtension(theme)) });
});

onBeforeUnmount(() => {
  view?.destroy();
  view = undefined;
});
</script>

<template>
  <div ref="host" class="values-code-editor" :aria-label="t('values.editor.aria')" />
</template>

<style scoped>
.values-code-editor {
  overflow: hidden;
  border: 1px solid var(--color-text-secondary);
  border-radius: 0.75rem;
}
</style>
