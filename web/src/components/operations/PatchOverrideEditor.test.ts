import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import PatchOverrideEditor from './PatchOverrideEditor.vue';
import type { PatchOverride } from '@/types/operation';

/*
 * W12 (UX-024). The editor's contract has always been "error + optional index": a
 * validation error that names a row renders inside that row, and everything else is
 * the fieldset's. The second half was missing — the row message is gated on
 * `errorIndex === index`, so an error WITHOUT an index rendered nowhere at all, which
 * is the worst failure mode for a form (it refuses to submit and shows nothing).
 *
 * The unindexed path has no producer in `stores/operationForm.ts` today (every patch
 * error carries an index), so these cases mount the component directly: they pin the
 * rendering contract that the next producer will rely on.
 */
function rows(): PatchOverride[] {
  return [{ path: 'image.tag', value: 'v2', kind: 'LITERAL' }];
}

function mountEditor(props: { error?: string; errorIndex?: number } = {}) {
  return mount(PatchOverrideEditor, {
    props: { modelValue: rows(), ...props },
  });
}

describe('PatchOverrideEditor error placement', () => {
  it('renders an indexed error inside its row', () => {
    const wrapper = mountEditor({ error: 'Patch 引用了 Bundle 外镜像', errorIndex: 0 });

    const row = wrapper.get('.patch-editor__row');
    expect(row.classes()).toContain('patch-editor__row--error');
    const message = row.get('.patch-editor__row-error');
    expect(message.text()).toBe('Patch 引用了 Bundle 外镜像');
    expect(message.attributes('role')).toBe('alert');
    // The row-level message is what the focus-first-error search must not need to find.
    expect(wrapper.find('[data-field-error]').exists()).toBe(false);
    expect(wrapper.get('input[aria-label="补丁 1 路径"]').attributes('aria-invalid')).toBe('true');
  });

  it('renders an error without an index as a fieldset-level alert instead of dropping it', () => {
    const wrapper = mountEditor({ error: '请先选择制品，再检查 Patch 路径' });

    const summary = wrapper.get('[data-field-error]');
    expect(summary.text()).toBe('请先选择制品，再检查 Patch 路径');
    expect(summary.attributes('role')).toBe('alert');
    // Focusable, so the form's "focus the first error" step can land on it.
    expect(summary.attributes('tabindex')).toBe('-1');
    // It is a summary, not a row error: no row is marked invalid by it.
    expect(wrapper.find('.patch-editor__row-error').exists()).toBe(false);
    expect(wrapper.find('.patch-editor__row--error').exists()).toBe(false);
    // The component binds `:aria-invalid` to a boolean, so Vue renders "false" rather
    // than omitting the attribute — `[aria-invalid="true"]` still never matches it.
    expect(wrapper.get('input[aria-label="补丁 1 路径"]').attributes('aria-invalid')).toBe('false');
  });

  it('renders nothing when the error is cleared (the summary cannot outlive its message)', async () => {
    const wrapper = mountEditor({ error: '请先选择制品', errorIndex: 0 });
    expect(wrapper.find('.patch-editor__row-error').exists()).toBe(true);

    await wrapper.setProps({ error: undefined, errorIndex: undefined });
    expect(wrapper.find('.patch-editor__row-error').exists()).toBe(false);
    expect(wrapper.find('[data-field-error]').exists()).toBe(false);
  });
});

describe('PatchOverrideEditor blur reporting', () => {
  it('reports a blur from either control so the form can validate on the way out', async () => {
    const wrapper = mountEditor();

    await wrapper.get('input[aria-label="补丁 1 路径"]').trigger('blur');
    await wrapper.get('input[aria-label="补丁 1 值"]').trigger('blur');

    expect(wrapper.emitted('blur')).toHaveLength(2);
  });
});
