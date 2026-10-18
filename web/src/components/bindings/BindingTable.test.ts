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
});
