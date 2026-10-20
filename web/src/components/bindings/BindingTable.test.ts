import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import BindingTable from './BindingTable.vue';
import type { BindingView } from '@/connect/binding-api';

/*
 * TASK-363: the bindings table, pinned as a presentational contract.
 * The page owns what "revoke" means; the table reports which row asked for it.
 */

const ACTIVE: BindingView = {
  id: 'b1',
  orgId: 'org-1',
  customerId: 'customer-a',
  status: 'active',
  optimisticVersion: 2n,
  createdAt: '2026-09-28T00:00:00Z',
  updatedAt: null,
};
const REVOKED: BindingView = { ...ACTIVE, id: 'b2', customerId: 'customer-b', status: 'revoked', createdAt: null };

function mountTable(props: { bindings?: BindingView[]; saving?: boolean } = {}) {
  return mount(BindingTable, { props: { bindings: [ACTIVE, REVOKED], saving: false, ...props } });
}

describe('BindingTable', () => {
  it('renders the columns and each row status with the original classes', () => {
    const wrapper = mountTable();

    const headers = Array.from((wrapper.element as HTMLElement).querySelectorAll<HTMLElement>('th')).map((th) => th.textContent?.trim());
    expect(headers).toEqual(['客户', '状态', '版本', '建立时间', '操作']);
    expect(wrapper.get('[data-testid="binding-row-customer-a"]').text()).toContain('生效中');
    expect(wrapper.get('[data-testid="binding-row-customer-a"] .governance__status').classes()).toContain(
      'governance__status--active',
    );
    expect(wrapper.get('[data-testid="binding-row-customer-b"]').text()).toContain('已撤销');
    expect(wrapper.get('[data-testid="binding-row-customer-b"] .governance__status').classes()).toContain(
      'governance__status--revoked',
    );
    expect(wrapper.get('[data-testid="binding-row-customer-a"] td:nth-child(3)').text()).toBe('2');
    expect(wrapper.get('[data-testid="binding-row-customer-a"] td:nth-child(4)').text()).toMatch(/2026/);
    expect(wrapper.get('[data-testid="binding-row-customer-b"] td:nth-child(4)').text()).toBe('—');
  });

  it('offers revoke only on an active row', () => {
    const wrapper = mountTable();

    expect(wrapper.find('[data-testid="binding-row-customer-a"] button').exists()).toBe(true);
    expect(wrapper.find('[data-testid="binding-row-customer-b"] button').exists()).toBe(false);
    expect(wrapper.get('[data-testid="binding-row-customer-b"] td:last-child span').classes()).toContain(
      'governance__muted',
    );
  });

  it("emits the row's binding when revoke is clicked", async () => {
    const wrapper = mountTable();

    await wrapper.get('[data-testid="binding-row-customer-a"] button').trigger('click');

    expect(wrapper.emitted('revoke')).toEqual([[ACTIVE]]);
  });

  it('disables the revoke buttons while a write is in flight', () => {
    const wrapper = mountTable({ saving: true });

    expect((wrapper.get('[data-testid="binding-row-customer-a"] button').element as HTMLButtonElement).disabled).toBe(true);
  });

  /*
   * TASK-394: happy-dom does not evaluate scoped CSS, so the direction-C table contract is
   * asserted against the component source — the convention the audit tables use for their
   * stylesheet invariants. The frame moved from the <table> to the `.table-wrap` scroller so
   * the rounded corners and the surface are one box.
   */
  it('frames the scroller and paints the header from the direction-C tokens', () => {
    const source = readFileSync(resolve(__dirname, 'BindingTable.vue'), 'utf8');
    const style = source.replace(/\/\*[\s\S]*?\*\//g, '').match(/<style scoped>([\s\S]*?)<\/style>/)?.[1] ?? '';

    expect(style).toMatch(/\.table-wrap\s*\{[^}]*border:\s*1px solid var\(--color-border-strong\)/);
    expect(style).toMatch(/\.table-wrap\s*\{[^}]*background:\s*var\(--color-surface\)/);
    expect(style).toMatch(/\.table-wrap\s*\{[^}]*border-radius:\s*var\(--r\)/);
    expect(style).toMatch(/\.governance__table th\s*\{[^}]*background:\s*var\(--color-surface-muted\)/);
    expect(style).toMatch(/\.governance__table th\s*\{[^}]*color:\s*var\(--color-muted-strong\)/);
    expect(style).toMatch(/\.governance__table th\s*\{[^}]*font-size:\s*var\(--font-size-xs\)/);
    // The table must not draw a second border inside the frame.
    expect(style).not.toMatch(/\.governance__table\s*\{[^}]*border:/);
  });
});
