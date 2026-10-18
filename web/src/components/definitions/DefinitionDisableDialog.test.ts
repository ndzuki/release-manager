import { mount } from '@vue/test-utils';
import { afterEach, describe, expect, it } from 'vitest';
import { nextTick } from 'vue';
import DefinitionDisableDialog from './DefinitionDisableDialog.vue';

/*
 * TASK-363: the disable confirmation was carved out of DefinitionsPage. The page
 * renders it only while a definition is armed, so this component is always open and
 * only reports intent; the destructive RPC is the page's decision.
 */

const mounted: Array<{ unmount: () => void }> = [];

afterEach(() => {
  for (const wrapper of mounted.splice(0)) wrapper.unmount();
  document.body.innerHTML = '';
});

function mountDisable(overrides: { failure?: string | null; saving?: boolean } = {}) {
  const wrapper = mount(DefinitionDisableDialog, {
    props: {
      description: '确认后将停用定义 target：不再有新的操作可以指向它，既有历史与清单会保留。',
      failure: overrides.failure ?? null,
      saving: overrides.saving ?? false,
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

describe('DefinitionDisableDialog', () => {
  it('renders a danger alertdialog with the title and the named description', () => {
    mountDisable();

    const dialog = document.querySelector<HTMLElement>('[role="alertdialog"]');
    expect(dialog).not.toBeNull();
    const titleId = dialog?.getAttribute('aria-labelledby');
    expect(document.getElementById(titleId ?? '')?.textContent).toBe('禁用发布定义');
    expect(dialog?.querySelector('.app-dialog__description')?.textContent).toContain('target');
    expect(button(dialog!, '确认禁用')).toBeTruthy();
  });

  it('keeps the failure reason inside the still-open dialog as an alert', () => {
    mountDisable({ failure: '定义已被其他人修改（版本冲突），已刷新最新数据，请重试' });

    const dialog = document.querySelector<HTMLElement>('[role="alertdialog"]');
    const failure = dialog?.querySelector<HTMLElement>('[data-testid="definitions-disable-error"]');
    expect(failure?.textContent?.trim()).toBe('定义已被其他人修改（版本冲突），已刷新最新数据，请重试');
    expect(failure?.getAttribute('role')).toBe('alert');
  });

  it('reports confirm and cancel as events', async () => {
    const wrapper = mountDisable();

    button(document, '确认禁用').click();
    await nextTick();
    expect(wrapper.emitted('confirm')).toHaveLength(1);

    button(document, '取消').click();
    await nextTick();
    expect(wrapper.emitted('close')).toHaveLength(1);
  });

  it('disables both actions while the disable is in flight', () => {
    mountDisable({ saving: true });

    expect(button(document, '取消').disabled).toBe(true);
    expect(button(document, '禁用中…').disabled).toBe(true);
  });
});
