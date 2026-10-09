import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import { h } from 'vue';
import DataTable from './DataTable.vue';

/*
 * DataTable's contract is three async states plus a sortable header that a keyboard
 * can operate. The sort assertions read the rendered row order AND `aria-sort`,
 * because `aria-sort` is the only part of the state a screen reader receives.
 *
 * Note on "keyboard trigger": happy-dom does not synthesise the click a browser
 * fires for Enter/Space on a <button>, so the keyboard test asserts the two things
 * that make that activation work (a native <button> and focusability) and then
 * triggers the click the keyboard path dispatches. It does not pretend happy-dom
 * implements the activation itself.
 */
const columns = [
  { key: 'name', label: '名称', sortable: true },
  { key: 'replicas', label: '副本数', sortable: true, align: 'end' as const },
  { key: 'state', label: '状态' },
];

const rows = [
  { id: 'a', name: 'beta', replicas: 2, state: 'ready' },
  { id: 'b', name: 'alpha', replicas: 10, state: 'pending' },
];

interface TableOverrides {
  rows?: Array<Record<string, unknown>>;
  loading?: boolean;
  empty?: string;
  error?: { message: string; details?: string; retryable?: boolean } | null;
}

function mountTable(overrides: TableOverrides = {}) {
  return mount(DataTable, { props: { columns, rows, rowKey: 'id', ...overrides } });
}

function bodyRows(wrapper: ReturnType<typeof mountTable>): string[][] {
  return wrapper
    .findAll('tbody tr')
    .map((row) => row.findAll('td').map((cell) => cell.text()));
}

describe('DataTable', () => {
  it('renders the headers with their alignment and the rows it was given', () => {
    const wrapper = mountTable();

    expect(wrapper.findAll('th').map((header) => header.text())).toEqual(['名称', '副本数', '状态']);
    expect(wrapper.get('th[data-key="replicas"]').classes()).toContain('data-table__cell--end');
    expect(bodyRows(wrapper)).toEqual([
      ['beta', '2', 'ready'],
      ['alpha', '10', 'pending'],
    ]);
  });

  it('sorts a sortable column on click and moves aria-sort with it', async () => {
    const wrapper = mountTable();
    const nameHeader = wrapper.get('th[data-key="name"]');

    await nameHeader.get('button').trigger('click');

    expect(bodyRows(wrapper).map((cells) => cells[0])).toEqual(['alpha', 'beta']);
    expect(nameHeader.attributes('aria-sort')).toBe('ascending');
    // Inactive sortable columns advertise "none"; a non-sortable one has no attribute.
    expect(wrapper.get('th[data-key="replicas"]').attributes('aria-sort')).toBe('none');
    expect(wrapper.get('th[data-key="state"]').attributes('aria-sort')).toBeUndefined();
  });

  it('toggles to descending on a second click and emits the sort', async () => {
    const wrapper = mountTable();
    const button = wrapper.get<HTMLButtonElement>('th[data-key="name"] button');

    await button.trigger('click');
    await button.trigger('click');

    expect(bodyRows(wrapper).map((cells) => cells[0])).toEqual(['beta', 'alpha']);
    expect(wrapper.get('th[data-key="name"]').attributes('aria-sort')).toBe('descending');
    expect(wrapper.emitted('sort')).toEqual([
      [{ key: 'name', direction: 'asc' }],
      [{ key: 'name', direction: 'desc' }],
    ]);
  });

  it('sorts numbers numerically rather than as strings', async () => {
    const wrapper = mountTable();

    await wrapper.get('th[data-key="replicas"] button').trigger('click');

    expect(bodyRows(wrapper).map((cells) => cells[1])).toEqual(['2', '10']);
  });

  it('keeps missing values last and renders them as empty cells', async () => {
    const wrapper = mountTable({
      rows: [
        { id: 'a', name: 'beta', replicas: 2, state: 'ready' },
        { id: 'b', name: null, replicas: 1, state: 'unknown' },
        { id: 'c', name: 'alpha', replicas: 3, state: 'ready' },
      ],
    });

    await wrapper.get('th[data-key="name"] button').trigger('click');

    expect(bodyRows(wrapper).map((cells) => cells[0])).toEqual(['alpha', 'beta', '']);
  });

  it('exposes sortable headers as focusable native buttons', async () => {
    // Attached to the document: focus() is a no-op on a detached element, and this
    // is the one test that asserts real focus.
    const wrapper = mount(DataTable, {
      attachTo: document.body,
      props: { columns, rows, rowKey: 'id' },
    });

    const button = wrapper.get<HTMLButtonElement>('th[data-key="name"] button');
    expect(button.element.tagName).toBe('BUTTON');
    expect(button.attributes('type')).toBe('button');
    expect(wrapper.find('th[data-key="state"] button').exists()).toBe(false);

    // Tab reaches it…
    button.element.focus();
    expect(document.activeElement).toBe(button.element);

    // …and the click a browser synthesises from Enter/Space is what changes the sort.
    await button.trigger('click');
    expect(wrapper.get('th[data-key="name"]').attributes('aria-sort')).toBe('ascending');

    wrapper.unmount();
  });

  it('renders the loading state instead of the table', () => {
    const wrapper = mountTable({ loading: true });

    expect(wrapper.get('[role="status"]').attributes('aria-label')).toBe('加载中');
    expect(wrapper.find('table').exists()).toBe(false);
  });

  it('renders the empty state with its message, and lets the owner replace it', () => {
    const wrapper = mountTable({ rows: [], empty: '这个范围内没有定义。' });

    expect(wrapper.get('.empty-state').text()).toContain('暂无数据');
    expect(wrapper.get('.empty-state').text()).toContain('这个范围内没有定义。');
    expect(wrapper.find('table').exists()).toBe(false);

    const replaced = mount(DataTable, {
      props: { columns, rows: [], empty: 'ignored' },
      slots: { empty: () => h('p', { class: 'custom-empty' }, '自定义空态') },
    });
    expect(replaced.get('.custom-empty').text()).toBe('自定义空态');
    expect(replaced.find('.empty-state').exists()).toBe(false);
  });

  it('renders the error state and only offers retry when the error is retryable', async () => {
    const wrapper = mountTable({ error: { message: '加载失败，请稍后重试。', details: 'grpc: unavailable' } });

    expect(wrapper.get('[role="alert"]').text()).toContain('加载失败，请稍后重试。');
    expect(wrapper.find('table').exists()).toBe(false);
    expect(wrapper.find('[role="alert"] button').exists()).toBe(false);

    const retryable = mountTable({ error: { message: '加载失败。', retryable: true } });
    await retryable.get('[role="alert"] button').trigger('click');
    expect(retryable.emitted('retry')).toHaveLength(1);
  });

  it('gives the error state precedence over loading and rows', () => {
    const wrapper = mountTable({ loading: true, error: { message: '出错了。' } });

    expect(wrapper.find('[role="alert"]').exists()).toBe(true);
    expect(wrapper.find('[role="status"]').exists()).toBe(false);
    expect(wrapper.find('table').exists()).toBe(false);
  });

  it('gives the loading state precedence over rows already in hand', () => {
    const wrapper = mountTable({ loading: true });

    expect(wrapper.find('[role="status"]').exists()).toBe(true);
    expect(wrapper.find('table').exists()).toBe(false);
  });

  it('renders a cell through the cell slot with its row, column and value', () => {
    const wrapper = mount(DataTable, {
      props: { columns, rows, rowKey: 'id' },
      slots: {
        cell: (slotProps) =>
          slotProps.column.key === 'state'
            ? h('span', { class: 'state-badge' }, `badge:${String(slotProps.value)}`)
            : h('span', String(slotProps.value)),
      },
    });

    expect(wrapper.findAll('.state-badge').map((badge) => badge.text())).toEqual([
      'badge:ready',
      'badge:pending',
    ]);
  });
});
