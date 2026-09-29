import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import ReleaseInventorySkeleton from './ReleaseInventorySkeleton.vue';

describe('ReleaseInventorySkeleton', () => {
  // The label used to be written as aria-label="{{ t(...) }}", which Vue 3 renders as
  // the literal braces; a gate now forbids that form, and this case pins the value.
  it('announces loading through the catalog label', () => {
    const wrapper = mount(ReleaseInventorySkeleton);

    expect(wrapper.get('[role="status"]').attributes('aria-label')).toBe('正在加载 Release 列表…');
    expect(wrapper.get('[role="status"]').attributes('aria-label')).not.toContain('{{');
  });
});
