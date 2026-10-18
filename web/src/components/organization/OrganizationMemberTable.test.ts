import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import type { OrganizationMemberView } from '@/connect/organization-api';
import OrganizationMemberTable from './OrganizationMemberTable.vue';

/*
 * TASK-363: the member list was carved out of OrganizationPage. Its role dropdown
 * and remove button are the page's only entry points into the membership writes, so
 * the wiring (which row an action reports, which options are grantable, what a
 * read-only caller sees) is pinned here instead of through the whole page.
 */

function member(overrides: Partial<OrganizationMemberView> = {}): OrganizationMemberView {
  return {
    userId: 'viewer-1',
    role: 'viewer',
    optimisticVersion: 1n,
    createdAt: '2026-09-28T00:00:00Z',
    updatedAt: null,
    ...overrides,
  };
}

function mountTable(
  members: OrganizationMemberView[],
  overrides: {
    canWrite?: boolean;
    saving?: boolean;
    grantableRoles?: OrganizationMemberView['role'][];
  } = {},
) {
  return mount(OrganizationMemberTable, {
    props: {
      members,
      canWrite: overrides.canWrite ?? true,
      saving: overrides.saving ?? false,
      grantableRoles: overrides.grantableRoles ?? ['platform_admin', 'release_admin', 'deployer', 'viewer'],
    },
  });
}

describe('OrganizationMemberTable', () => {
  it('renders the columns and the row values', () => {
    const wrapper = mountTable([
      member({ userId: 'admin-1', role: 'platform_admin', optimisticVersion: 3n }),
      member({ userId: 'empty-1', createdAt: null }),
    ]);

    expect(wrapper.findAll('th').map((header) => header.text())).toEqual([
      '用户',
      '角色',
      '版本',
      '加入时间',
      '操作',
    ]);

    const cells = wrapper.get('[data-testid="member-row-admin-1"]').findAll('td');
    expect(cells[0]?.get('code').text()).toBe('admin-1');
    expect(cells[1]?.get('.organization__role--platform_admin').text()).toBe('platform_admin');
    expect(cells[2]?.text()).toBe('3');
    expect(cells[3]?.text()).not.toBe('');
    // `createdAt: null` still renders the shared formatter's dash.
    expect(wrapper.get('[data-testid="member-row-empty-1"]').findAll('td')[3]?.text()).toBe('—');
  });

  it('reports the row and the native event a role change came from', async () => {
    const row = member();
    const wrapper = mountTable([row]);

    const select = wrapper.get('select[id="role-viewer-1"]');
    await select.setValue('deployer');

    const emitted = wrapper.emitted('change-role');
    expect(emitted).toHaveLength(1);
    expect(emitted?.[0]?.[0]).toEqual(row);
    expect(emitted?.[0]?.[1]).toBeInstanceOf(Event);
  });

  it('reports the row a removal was requested for', async () => {
    const row = member();
    const wrapper = mountTable([row]);

    await wrapper.get('[data-testid="member-row-viewer-1"] button').trigger('click');

    expect(wrapper.emitted('remove')?.[0]?.[0]).toEqual(row);
  });

  it('disables the role options the caller cannot grant', () => {
    const wrapper = mountTable([member()], { grantableRoles: ['release_admin', 'deployer', 'viewer'] });

    expect(wrapper.get<HTMLOptionElement>('option[value="platform_admin"]').element.disabled).toBe(true);
    expect(wrapper.get<HTMLOptionElement>('option[value="deployer"]').element.disabled).toBe(false);
  });

  it('shows the read-only marker instead of the controls for a caller that can grant nothing', () => {
    const wrapper = mountTable([member()], { canWrite: false });

    expect(wrapper.find('select').exists()).toBe(false);
    expect(wrapper.find('button').exists()).toBe(false);
    expect(wrapper.get('.organization__readonly').text()).toBe('只读');
  });

  it('disables both controls while a membership write is in flight', () => {
    const wrapper = mountTable([member()], { saving: true });

    expect(wrapper.get<HTMLSelectElement>('select[id="role-viewer-1"]').element.disabled).toBe(true);
    expect(wrapper.get<HTMLButtonElement>('button').element.disabled).toBe(true);
  });

  /*
   * TASK-367: a member row (UUID, role, version, timestamp, role select, remove
   * button) is wider than 375px. The fix is the same labelled, focusable scroll
   * region as the other wide tables; the page must not scroll sideways instead.
   */
  it('wraps the table in a labelled, focusable horizontal scroll region', () => {
    const wrapper = mountTable([member()]);

    const region = wrapper.get('[data-testid="organization-table-scroll"]');
    expect(region.attributes('role')).toBe('region');
    expect(region.attributes('aria-label')).toBe('组织成员表格（可横向滚动）');
    expect(region.attributes('tabindex')).toBe('0');
    expect(region.find('table.organization__table').exists()).toBe(true);
  });
});
