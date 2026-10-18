import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import { nextTick } from 'vue';
import type { PromotionMappingView } from '@/connect/definition-api';
import DefinitionEditorDialog from './DefinitionEditorDialog.vue';

/*
 * TASK-363: the editor dialog was carved out of DefinitionsPage. AppDialog teleports
 * to document.body, so the content is queried there (the pattern the page tests use
 * for every overlay). The page owns the draft; this component owns the markup, the
 * per-instance control ids and the intent events.
 */

const mounted: Array<{ unmount: () => void }> = [];

afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
});

function mapping(overrides: Partial<PromotionMappingView> = {}): PromotionMappingView {
  return { workloadKind: 'Deployment', workloadName: 'api', container: 'app', field: 'image', valuesPath: 'image.tag', ...overrides };
}

function mountEditor(overrides: {
  open?: boolean;
  saving?: boolean;
  validationError?: string;
  mappings?: PromotionMappingView[];
} = {}) {
  const wrapper = mount(DefinitionEditorDialog, {
    props: {
      open: overrides.open ?? true,
      title: '编辑 target（版本 3）',
      saving: overrides.saving ?? false,
      validationError: overrides.validationError ?? '',
      namespace: 'ns',
      releaseName: 'release-a',
      chartName: 'chart',
      mappings: overrides.mappings ?? [],
    },
  });
  mounted.push(wrapper);
  return wrapper;
}

function button(root: ParentNode, label: string): HTMLButtonElement {
  const element = Array.from(root.querySelectorAll<HTMLButtonElement>('button')).find(
    (candidate) => candidate.textContent?.trim() === label,
  );
  if (!element) throw new Error(`button not found: ${label}`);
  return element;
}

describe('DefinitionEditorDialog', () => {
  it('renders the labelled dialog, the prefilled identifiers and the mapping rows', () => {
    mountEditor({ mappings: [mapping()] });

    const panel = document.querySelector<HTMLElement>('.app-dialog__panel');
    expect(panel).not.toBeNull();
    const titleId = panel?.getAttribute('aria-labelledby');
    expect(document.getElementById(titleId ?? '')?.textContent).toBe('编辑 target（版本 3）');

    expect(document.querySelector<HTMLInputElement>('.definitions__editor input[name="namespace"]')?.value).toBe('ns');
    expect(document.querySelector<HTMLInputElement>('.definitions__editor input[name="releaseName"]')?.value).toBe('release-a');
    expect(document.querySelector<HTMLInputElement>('.definitions__editor input[name="chartName"]')?.value).toBe('chart');
    expect(document.querySelectorAll('[data-testid^="mapping-row-"]')).toHaveLength(1);
    expect(document.querySelector<HTMLInputElement>('.definitions__editor input[name="kind-0"]')?.value).toBe('Deployment');
  });

  it('labels every mapping input by column and row number', () => {
    mountEditor({ mappings: [mapping(), mapping({ workloadKind: 'StatefulSet' })] });

    const inputs = Array.from(document.querySelectorAll<HTMLInputElement>('.definitions__mappings tbody input'));
    expect(inputs).toHaveLength(10);
    for (const input of inputs) {
      const id = input.getAttribute('id');
      expect(id).toBeTruthy();
      const label = document.querySelector(`.definitions__mappings label[for="${id}"]`);
      expect(label?.textContent?.trim()).not.toBe('');
    }
    expect(document.querySelectorAll('.definitions__mappings label[for$="-0-kind"]')[0]?.textContent?.trim())
      .toBe('Workload 类型（第 1 行）');
    expect(document.querySelectorAll('.definitions__mappings label[for$="-1-kind"]')[0]?.textContent?.trim())
      .toBe('Workload 类型（第 2 行）');
  });

  it('edits the draft it was handed in place (the page owns it)', async () => {
    const row = mapping();
    mountEditor({ mappings: [row] });

    const input = document.querySelector<HTMLInputElement>('.definitions__editor input[name="kind-0"]')!;
    input.value = 'StatefulSet';
    input.dispatchEvent(new Event('input'));
    await nextTick();

    expect(row.workloadKind).toBe('StatefulSet');
  });

  it('reports add/remove mapping, submit and close as events', async () => {
    const wrapper = mountEditor({ mappings: [mapping()] });

    button(document, '新增映射').click();
    await nextTick();
    expect(wrapper.emitted('add-mapping')).toHaveLength(1);

    document.querySelector<HTMLButtonElement>('[data-testid="mapping-remove-0"]')!.click();
    await nextTick();
    expect(wrapper.emitted('remove-mapping')?.[0]).toEqual([0]);

    document.querySelector<HTMLFormElement>('.definitions__editor form')!.dispatchEvent(new Event('submit'));
    await nextTick();
    expect(wrapper.emitted('submit')).toHaveLength(1);

    button(document, '取消').click();
    await nextTick();
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('renders the validation message as an alert', () => {
    mountEditor({ validationError: '第 1 行映射缺少必填项' });

    const alert = document.querySelector<HTMLElement>('[data-testid="definitions-validation"]');
    expect(alert?.textContent?.trim()).toBe('第 1 行映射缺少必填项');
    expect(alert?.getAttribute('role')).toBe('alert');
  });

  it('keeps Escape from closing the dialog while a save is in flight', async () => {
    const wrapper = mountEditor({ saving: true });

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await nextTick();
    expect(wrapper.emitted('close')).toBeUndefined();

    await wrapper.setProps({ saving: false });
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await nextTick();
    expect(wrapper.emitted('close')).toHaveLength(1);
  });
});
