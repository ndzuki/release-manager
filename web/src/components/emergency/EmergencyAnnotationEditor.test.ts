import { mount, type VueWrapper } from '@vue/test-utils';
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

/*
 * TASK-272. The row counter used to be a fresh `ref(1)` per mount and never
 * looked at the seeded rows, so an editor opened with `local-1`/`local-2` minted
 * another `local-1` on Add. `localId` is the `v-for` key and the update/remove
 * lookup key, so a duplicate row id makes Vue patch one row's DOM for both —
 * which is exactly what the label/for wiring above cannot survive. These tests
 * feed each emitted row list back into `values` through `setProps`, the way the
 * page's `:values` + `@update` controlled-component binding does.
 */
describe('EmergencyAnnotationEditor local row ids (TASK-272)', () => {
  type Row = { localId: string; key: string; value: string; scope: string };

  const SEEDED: Row[] = [
    { localId: 'local-1', key: 'owner', value: 'team-a', scope: 'cluster' },
    { localId: 'local-2', key: 'ticket', value: '', scope: 'cluster' },
  ];

  // The editor is a controlled component: it only ever emits the whole row list,
  // so each test feeds every emit back through `setProps`, the way the page does.
  function mountWithRows(seed: Row[]): VueWrapper {
    return mount(EmergencyAnnotationEditor, {
      props: {
        approvedKeys: ['owner', 'ticket', 'zone'],
        scope: 'cluster',
        values: seed.map((row) => ({ ...row })),
      },
    });
  }

  // The add button is the editor's only direct button child (the per-row removes
  // live inside the table), so this selector cannot hit a row action.
  async function addRow(wrapper: VueWrapper): Promise<void> {
    await wrapper.get('.annotation-editor > button').trigger('click');
  }

  function emittedRows(wrapper: VueWrapper): Row[] {
    const events = wrapper.emitted('update');
    expect(events, 'Add/remove must emit the whole row list').toBeTruthy();
    const last = events?.at(-1);
    expect(last, 'the editor must have emitted at least once').toBeTruthy();
    return (last?.[0] ?? []) as Row[];
  }

  // Feed the last emitted row list back in as props (the page's `:values` +
  // `@update` controlled-component binding).
  async function applyEmittedRows(wrapper: VueWrapper): Promise<void> {
    await wrapper.setProps({ values: emittedRows(wrapper) });
  }

  function controlIds(wrapper: VueWrapper): string[] {
    return wrapper
      .findAll('select.field-input, input.field-input')
      .map((control) => control.attributes('id') ?? '');
  }

  it('mints a fresh localId after seeded rows instead of restarting at 1', async () => {
    const wrapper = mountWithRows(SEEDED);

    await addRow(wrapper);

    const ids = emittedRows(wrapper).map((row) => row.localId);
    // The seeded rows keep their ids, and exactly one id is appended for the new
    // row (which fixes the total length without pinning the appended id).
    expect(ids.slice(0, SEEDED.length)).toEqual(SEEDED.map((row) => row.localId));
    const appended = ids.slice(SEEDED.length);
    expect(appended).toHaveLength(1);
    // The new id is not one of the ids already there — that is the whole fix —
    // and no two rows may share a localId (the v-for key / row-lookup key).
    expect(SEEDED.map((row) => row.localId)).not.toContain(appended[0]);
    expect(new Set(ids).size).toBe(ids.length);

    await applyEmittedRows(wrapper);

    // …and the rendered controls' DOM ids stay unique, so every label[for] still
    // resolves to exactly one control.
    const domIds = controlIds(wrapper);
    expect(domIds).toHaveLength(6);
    expect(new Set(domIds).size).toBe(6);
    // The appended row rendered under the id it was given: its labels name the
    // third row's columns, so the label/for pair really points at that row.
    expect(wrapper.get(`label[for$="-${appended[0]}-key"]`).text()).toBe('Key（白名单）（第 3 行）');
    expect(wrapper.get(`label[for$="-${appended[0]}-value"]`).text()).toBe('值（第 3 行）');
  });

  it('continues the sequence from the highest seeded number', async () => {
    const wrapper = mountWithRows([{ localId: 'local-10', key: 'owner', value: 'x', scope: 'cluster' }]);

    await addRow(wrapper);

    const ids = emittedRows(wrapper).map((row) => row.localId);
    // A per-mount counter restarted at 1 and handed out `local-1` here, so this
    // pins the id: it must continue past the highest seeded number.
    expect(ids).toEqual(['local-10', 'local-11']);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it('treats a zero-padded seed number by its numeric value', async () => {
    const wrapper = mountWithRows([{ localId: 'local-007', key: 'owner', value: 'x', scope: 'cluster' }]);

    await addRow(wrapper);

    const ids = emittedRows(wrapper).map((row) => row.localId);
    expect(ids.slice(0, 1)).toEqual(['local-007']);
    expect(ids).toHaveLength(2);
    expect(new Set(ids).size).toBe(ids.length);
    // 007 is row 7, so the next id continues past 7 and must not be the fresh-1 a
    // per-mount counter would mint. `local-007` itself is not reused.
    expect(ids[1]).not.toBe('local-1');
    expect(ids[1]).not.toBe('local-007');
  });

  it('keeps ids unique when the seeded rows are not local-N shaped', async () => {
    const wrapper = mountWithRows([
      { localId: 'row-a', key: 'owner', value: 'x', scope: 'cluster' },
      { localId: 'row-b', key: 'ticket', value: 'y', scope: 'cluster' },
    ]);

    await addRow(wrapper);

    const ids = emittedRows(wrapper).map((row) => row.localId);
    expect(ids.slice(0, 2)).toEqual(['row-a', 'row-b']);
    expect(ids).toHaveLength(3);
    expect(new Set(ids).size).toBe(ids.length);
    // The appended id is not one of the seeded ids, even though neither of them
    // matches the local-N shape the search is built on.
    expect(['row-a', 'row-b']).not.toContain(ids[2]);
  });

  it('keeps ids unique after an existing row is removed', async () => {
    const wrapper = mountWithRows(SEEDED);

    await wrapper.findAll('button.row-remove')[0].trigger('click');
    await applyEmittedRows(wrapper);
    await addRow(wrapper);

    const ids = emittedRows(wrapper).map((row) => row.localId);
    expect(ids).toHaveLength(2);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it('starts a deleted-empty editor back at local-1', async () => {
    const wrapper = mountWithRows(SEEDED);

    await wrapper.findAll('button.row-remove')[0].trigger('click');
    await applyEmittedRows(wrapper);
    await wrapper.findAll('button.row-remove')[0].trigger('click');
    await applyEmittedRows(wrapper);
    await addRow(wrapper);

    const ids = emittedRows(wrapper).map((row) => row.localId);
    // Nothing left to continue from, so the sequence restarts at its base.
    expect(ids).toEqual(['local-1']);
  });

  it('does not repair a duplicate already in the seed; only the new id is guaranteed fresh', async () => {
    const wrapper = mountWithRows([
      { localId: 'local-1', key: 'owner', value: 'x', scope: 'cluster' },
      { localId: 'local-1', key: 'ticket', value: 'y', scope: 'cluster' },
    ]);

    await addRow(wrapper);

    const ids = emittedRows(wrapper).map((row) => row.localId);
    // Known boundary, frozen on purpose: this function only promises the NEW row
    // cannot collide. The seeded duplicate is rendered exactly as seeded, so two
    // of the three rows still share local-1 and the id set stays at 2.
    expect(ids).toEqual(['local-1', 'local-1', 'local-2']);
    expect(new Set(ids).size).toBe(2);
    expect(ids.at(-1)).not.toBe('local-1');
  });

  it('terminates when a seeded id sits past the 2^53 precision cliff', async () => {
    // 2^53: Number('9007199254740992') + 1 === Number('9007199254740992'), so a
    // candidate anchored there can never advance. A search that steps without a
    // bound spins forever inside the synchronous click handler and freezes the
    // whole test process, so this test fails by hanging without the fix.
    const wrapper = mountWithRows([
      { localId: 'local-9007199254740992', key: 'owner', value: 'x', scope: 'cluster' },
    ]);

    await addRow(wrapper);

    const ids = emittedRows(wrapper).map((row) => row.localId);
    // The unusable suffix is skipped, so the search falls back to the first free
    // number; the skipped seed id is preserved but never minted again.
    expect(ids.slice(0, 1)).toEqual(['local-9007199254740992']);
    expect(ids).toHaveLength(2);
    expect(ids[1]).not.toBe('local-9007199254740992');
    expect(new Set(ids).size).toBe(ids.length);
  });

  it('falls back to a free id when the candidate cannot advance past the cliff', async () => {
    // Row 9007199254740990 is a usable base (its successor is still safe), so the
    // candidate lands on 9007199254740991 (the max safe integer), which this seed
    // also occupies. The next step reaches 2^53 and then stops advancing: only the
    // bounded search plus the restart-from-1 fallback can terminate here.
    const wrapper = mountWithRows([
      { localId: 'local-9007199254740990', key: 'owner', value: 'x', scope: 'cluster' },
      { localId: 'local-9007199254740991', key: 'ticket', value: 'y', scope: 'cluster' },
      { localId: 'local-9007199254740992', key: 'zone', value: 'z', scope: 'cluster' },
    ]);

    await addRow(wrapper);

    const ids = emittedRows(wrapper).map((row) => row.localId);
    expect(ids.slice(0, 3)).toEqual([
      'local-9007199254740990',
      'local-9007199254740991',
      'local-9007199254740992',
    ]);
    expect(ids).toHaveLength(4);
    // The restart returns an id the seed does not already hold; without the
    // fallback the frozen candidate (2^53) would be handed out again and collide.
    expect(ids.slice(0, 3)).not.toContain(ids[3]);
    expect(new Set(ids).size).toBe(ids.length);
  });
});

