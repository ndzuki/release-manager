import { mount } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import type { DefinitionView } from '@/connect/definition-api';
import DefinitionTable from './DefinitionTable.vue';

/*
 * TASK-363: the definitions table was carved out of DefinitionsPage. Its row
 * actions are the page's only entry points into the destructive/editor dialogs, so
 * the wiring (which button renders for which status/permission, and what it
 * reports) is pinned here instead of through the whole page.
 */

function definition(overrides: Partial<DefinitionView> = {}): DefinitionView {
  return {
    id: 'def-1',
    name: 'target',
    customerId: 'cust-1',
    clusterId: 'cluster-1',
    namespace: 'ns',
    releaseName: 'release-a',
    chartName: 'chart',
    status: 'active',
    version: 3n,
    hpaManaged: false,
    maxEmergencyReplicas: 0,
    createdAt: '2026-09-28T00:00:00Z',
    updatedAt: null,
    promotionMappings: [],
    promotionMappingsViolation: null,
    approvedAnnotationKeys: [],
    approvedAnnotationKeysViolation: null,
    ...overrides,
  };
}

function mountTable(definitions: DefinitionView[], overrides: { canWrite?: boolean; saving?: boolean } = {}) {
  return mount(DefinitionTable, {
    props: {
      definitions,
      canWrite: overrides.canWrite ?? true,
      saving: overrides.saving ?? false,
    },
  });
}

describe('DefinitionTable', () => {
  it('renders the columns and the row values including the mapping count', () => {
    const wrapper = mountTable([
      definition({
        promotionMappings: [
          { workloadKind: 'Deployment', workloadName: 'api', container: 'app', field: 'image', valuesPath: 'image.tag' },
        ],
      }),
    ]);

    const headers = wrapper.findAll('th').map((header) => header.text());
    expect(headers).toEqual([
      '名称',
      '客户 / 集群',
      '命名空间 / Release',
      'Chart 名称',
      '状态',
      '版本',
      'Promotion Mapping（提升映射）',
      '操作',
    ]);

    const row = wrapper.get('[data-testid="definition-row-def-1"]');
    expect(row.text()).toContain('target');
    expect(row.text()).toContain('cust-1');
    expect(row.text()).toContain('cluster-1');
    expect(row.text()).toContain('ns / release-a');
    expect(row.text()).toContain('1 条');
    expect(row.text()).toContain('3');
    // `updatedAt: null` still renders the shared formatter's dash.
    expect(row.get('small').text()).toBe('—');
  });

  it('flags a contract violation and disables its editor', () => {
    const wrapper = mountTable([definition({ promotionMappingsViolation: '无法解析' })]);

    const row = wrapper.get('[data-testid="definition-row-def-1"]');
    expect(row.text()).toContain('无法解析');
    expect(wrapper.get<HTMLButtonElement>('[data-testid="definition-edit-def-1"]').element.disabled).toBe(true);
  });

  it('reports the row an action was requested for', async () => {
    const row = definition();
    const wrapper = mountTable([row]);

    await wrapper.get('[data-testid="definition-edit-def-1"]').trigger('click');
    await wrapper.get('[data-testid="definition-disable-def-1"]').trigger('click');

    expect(wrapper.emitted('edit')?.[0]?.[0]).toEqual(row);
    expect(wrapper.emitted('disable')?.[0]?.[0]).toEqual(row);
  });

  it('hides every write action for a read-only role', () => {
    const wrapper = mountTable([definition()], { canWrite: false });

    expect(wrapper.find('[data-testid="definition-edit-def-1"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="definition-disable-def-1"]').exists()).toBe(false);
  });

  it('does not offer disable for a terminal (disabled) definition', () => {
    const wrapper = mountTable([definition({ status: 'disabled' })]);

    expect(wrapper.find('[data-testid="definition-disable-def-1"]').exists()).toBe(false);
    expect(wrapper.text()).toContain('已停用');
  });

  it('disables the disable action while a write is in flight', () => {
    const wrapper = mountTable([definition()], { saving: true });

    expect(wrapper.get<HTMLButtonElement>('[data-testid="definition-disable-def-1"]').element.disabled).toBe(true);
  });
});
