import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import { defineComponent, h } from 'vue';
import EmergencyAnnotationEditor from './EmergencyAnnotationEditor.vue';

/*
 * TASK-269. The row editor renders its select and input inside bare <td> cells; the
 * column headers are plain <th> text, which is context for the CELL but not an
 * accessible name for the control inside it. Before this, a screen-reader user reaching
 * either control heard only "combo box" / "edit text". Each control must now own a
 * label whose `for` resolves to that control's id, and the row number must separate the
 * same column across rows.
 */
const PROPS = {
  approvedKeys: ['owner', 'ticket'],
  scope: 'cluster',
  values: [
    { localId: 'local-1', key: 'owner', value: 'team-a', scope: 'cluster' },
    { localId: 'local-2', key: 'ticket', value: '', scope: 'cluster' },
  ],
};

describe('EmergencyAnnotationEditor a11y wiring', () => {
  function mountEditor() {
    return mount(EmergencyAnnotationEditor, { props: PROPS });
  }

  it('labels every row control with its column and row', () => {
    const wrapper = mountEditor();

    const controls = wrapper.findAll('select.field-input, input.field-input');
    expect(controls).toHaveLength(4);

    for (const control of controls) {
      const id = control.attributes('id');
      expect(id, 'a control without an id cannot be pointed at by a label').toBeTruthy();
      expect(wrapper.get(`label[for="${id}"]`).text()).not.toBe('');
    }

    // Ids are unique across the two rows…
    expect(new Set(controls.map((control) => control.attributes('id'))).size).toBe(4);
    // …so the two rows' key selectors are distinguishable by their labels.
    expect(wrapper.get('label[for$="-local-1-key"]').text()).toBe('Key（白名单）（第 1 行）');
    expect(wrapper.get('label[for$="-local-1-value"]').text()).toBe('值（第 1 行）');
    expect(wrapper.get('label[for$="-local-2-key"]').text()).toBe('Key（白名单）（第 2 行）');
  });

  /*
   * Two editors in ONE app must not share ids — the same shape as FormField's stability
   * test. (Two editors mounted as separate Vue apps each start their useId counter at
   * zero and WOULD collide; one document with one app is the case the page can build,
   * and the page mounts one app.)
   */
  it('keeps two editors in one app from sharing ids', () => {
    const host = defineComponent({
      render() {
        return h('div', [h(EmergencyAnnotationEditor, PROPS), h(EmergencyAnnotationEditor, PROPS)]);
      },
    });
    const wrapper = mount(host);

    const ids = wrapper
      .findAll('select.field-input, input.field-input')
      .map((control) => control.attributes('id')!);

    expect(ids).toHaveLength(8);
    expect(new Set(ids).size).toBe(8);
    for (const id of ids) expect(wrapper.get(`label[for="${id}"]`).text()).not.toBe('');
  });
});

