<script setup lang="ts">
import { onBeforeUnmount, onMounted, useTemplateRef, watch } from 'vue';
import { Compartment, EditorState } from '@codemirror/state';
import { bracketMatching, defaultHighlightStyle, indentOnInput, syntaxHighlighting } from '@codemirror/language';
import { json } from '@codemirror/lang-json';
import { yaml } from '@codemirror/lang-yaml';
import { lintGutter, linter, type Diagnostic } from '@codemirror/lint';
import {
  EditorView,
  highlightActiveLine,
  highlightActiveLineGutter,
  lineNumbers,
} from '@codemirror/view';
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
let view: EditorView | undefined;

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
    source: 'values validation',
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
        syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
        languageCompartment.of(languageExtension(props.language)),
        readOnlyCompartment.of([EditorState.readOnly.of(Boolean(props.readOnly)), EditorView.editable.of(!props.readOnly)]),
        diagnosticsCompartment.of(diagnosticExtension()),
        EditorView.lineWrapping,
        EditorView.updateListener.of((update) => {
          if (update.docChanged) emit('update:modelValue', update.state.doc.toString());
        }),
        EditorView.theme({
          '&': { minHeight: '30rem', backgroundColor: '#0f172a', color: '#e2e8f0' },
          '.cm-content': { minHeight: '30rem', padding: '1rem', caretColor: '#f8fafc', fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', fontSize: '0.875rem' },
          '.cm-gutters': { backgroundColor: '#111827', color: '#64748b', border: 'none' },
          '.cm-activeLine': { backgroundColor: '#1e293b80' },
          '.cm-activeLineGutter': { backgroundColor: '#1e293b' },
          '.cm-scroller': { overflow: 'auto' },
        }, { dark: true }),
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

onBeforeUnmount(() => {
  view?.destroy();
  view = undefined;
});
</script>

<template>
  <div ref="host" class="values-code-editor" aria-label="Values document editor" />
</template>

<style scoped>
.values-code-editor {
  overflow: hidden;
  border: 1px solid #334155;
  border-radius: 0.75rem;
}
</style>
