import { computed, ref, type Ref } from 'vue';
import { t } from '@/i18n/messages';
import type { DefinitionView, PromotionMappingView, UpdateDefinitionInput } from '@/connect/definition-api';

/*
 * TASK-363: the editor's state and payload moved out of DefinitionsPage.
 *
 * The page only decides WHEN to open the editor (a row button); this composable
 * owns the draft, the validation and the versioned write. `update` and
 * `freshDefinition` are injected so the boundary conditions (the required mapping
 * fields, the trimming, and the version adoption after a conflict) can be tested
 * without mounting the page or faking Connect.
 */

export const BLANK_MAPPING: PromotionMappingView = {
  workloadKind: '',
  workloadName: '',
  container: '',
  field: '',
  valuesPath: '',
};

export interface DefinitionEditorOptions {
  /** The store's versioned update; returns false when the server refused it. */
  update: (input: UpdateDefinitionInput) => Promise<boolean>;
  /** The definition the store now holds, after the refusal reloaded the list. */
  freshDefinition: (definitionId: string) => DefinitionView | undefined;
}

export interface DefinitionEditor {
  /** The definition being edited; null when the dialog is closed. */
  editing: Ref<DefinitionView | null>;
  title: Ref<string>;
  validationError: Ref<string>;
  draftMappings: Ref<PromotionMappingView[]>;
  draftNamespace: Ref<string>;
  draftReleaseName: Ref<string>;
  draftChartName: Ref<string>;
  startEdit: (definition: DefinitionView) => void;
  close: () => void;
  addMapping: () => void;
  removeMapping: (index: number) => void;
  submit: () => Promise<boolean>;
}

export function useDefinitionEditor(options: DefinitionEditorOptions): DefinitionEditor {
  const editing = ref<DefinitionView | null>(null);
  const draftMappings = ref<PromotionMappingView[]>([]);
  const draftNamespace = ref('');
  const draftReleaseName = ref('');
  const draftChartName = ref('');
  const validationError = ref('');

  // TASK-281: the editor is an AppDialog, whose heading comes from this prop and
  // carries its own `aria-labelledby`; the title is the same text the hand-rolled
  // `<h2 id="definitions-editor-title">` used to render.
  const title = computed(() =>
    editing.value
      ? t('definitions.editorTitle', { name: editing.value.name, version: String(editing.value.version) })
      : '',
  );

  function startEdit(definition: DefinitionView): void {
    // An undecodable mapping cannot be written back: the row's button is disabled,
    // and the guard also covers a programmatic caller.
    if (definition.promotionMappingsViolation) return;
    editing.value = definition;
    draftMappings.value = definition.promotionMappings.map((mapping) => ({ ...mapping }));
    draftNamespace.value = definition.namespace;
    draftReleaseName.value = definition.releaseName;
    draftChartName.value = definition.chartName;
    validationError.value = '';
  }

  function close(): void {
    editing.value = null;
  }

  function addMapping(): void {
    draftMappings.value.push({ ...BLANK_MAPPING });
  }

  function removeMapping(index: number): void {
    draftMappings.value.splice(index, 1);
  }

  /*
   * TASK-214 removed the two refusals that used to live here. Clearing is expressible
   * again: the identifier strings are `optional string` (present-and-empty = clear) and
   * the mappings travel in a presence-carrying wrapper (an empty list = clear), so the
   * page can submit what the operator asked for instead of blocking it.
   */
  function validate(): boolean {
    for (const [index, mapping] of draftMappings.value.entries()) {
      if (!mapping.workloadKind.trim() || !mapping.workloadName.trim() || !mapping.field.trim() || !mapping.valuesPath.trim()) {
        validationError.value = `第 ${index + 1} 行映射缺少必填项（workload kind/name、field、values path）`;
        return false;
      }
    }
    validationError.value = '';
    return true;
  }

  function payload(target: DefinitionView): UpdateDefinitionInput {
    return {
      definitionId: target.id,
      // Always send the version we read: that is what makes the write versioned.
      expectedVersion: target.version,
      namespace: draftNamespace.value.trim(),
      releaseName: draftReleaseName.value.trim(),
      chartName: draftChartName.value.trim(),
      promotionMappings: draftMappings.value.map((mapping) => ({
        workloadKind: mapping.workloadKind.trim(),
        workloadName: mapping.workloadName.trim(),
        container: mapping.container.trim(),
        field: mapping.field.trim(),
        valuesPath: mapping.valuesPath.trim(),
      })),
    };
  }

  async function submit(): Promise<boolean> {
    const target = editing.value;
    if (!target || !validate()) return false;
    const ok = await options.update(payload(target));
    if (ok) {
      editing.value = null;
      return true;
    }
    // A conflict refreshed the list but left this editor on the stale object, so the
    // hinted retry would fail forever: adopt the version the server now holds. The
    // draft stays exactly as typed — only the version (and the title) move.
    const fresh = options.freshDefinition(target.id);
    if (fresh && fresh.version !== target.version) editing.value = fresh;
    return false;
  }

  return {
    editing,
    title,
    validationError,
    draftMappings,
    draftNamespace,
    draftReleaseName,
    draftChartName,
    startEdit,
    close,
    addMapping,
    removeMapping,
    submit,
  };
}
