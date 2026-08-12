import { mount } from '@vue/test-utils';
import { RouterLinkStub } from '@vue/test-utils';
import { describe, expect, it } from 'vitest';
import ReleaseStatusBadge from './ReleaseStatusBadge.vue';
import ReleaseInventoryTable from './ReleaseInventoryTable.vue';

describe('release inventory presentation', () => {
  it.each([
    ['missing', 'Release 已从集群中消失', 'Missing'],
    ['out_of_sync', '配置与期望不一致', 'Out of sync'],
  ] as const)('explains %s status without relying on color', (status, tooltip, label) => {
    const wrapper = mount(ReleaseStatusBadge, { props: { status } });

    expect(wrapper.attributes('title')).toBe(tooltip);
    expect(wrapper.text()).toContain(label);
  });

  it('keeps same-name releases distinct by namespace', () => {
    const wrapper = mount(ReleaseInventoryTable, {
      props: {
        releases: [
          { releaseDefinitionId: 'definition-a', namespace: 'apps', name: 'api', chart: 'api', chartVersion: '1.0.0', revision: 1, status: 'active', valuesDigest: 'sha256:a', lastSyncAt: null },
          { releaseDefinitionId: 'definition-b', namespace: 'system', name: 'api', chart: 'api', chartVersion: '1.0.0', revision: 2, status: 'missing', valuesDigest: 'sha256:b', lastSyncAt: null },
        ],
        customerId: 'customer-1',
        clusterId: 'cluster-1',
      },
    });

    expect(wrapper.text()).toContain('apps/api');
    expect(wrapper.text()).toContain('system/api');
  });

  it('shows the operation entry only for writable bound releases', () => {
    const release = {
      releaseDefinitionId: 'def-apps', namespace: 'apps', name: 'api', chart: 'api', chartVersion: '1.0.0',
      revision: 7, status: 'active' as const, valuesDigest: 'sha256:a', lastSyncAt: null,
    };
    const writable = mount(ReleaseInventoryTable, {
      props: {
        releases: [release],
        canCreateOperation: true,
        customerId: 'customer-1',
        clusterId: 'cluster-1',
        customerName: 'Customer 1',
        clusterName: 'Cluster 1',
      },
      global: { stubs: { RouterLink: RouterLinkStub } },
    });
    const links = writable.findAllComponents(RouterLinkStub);
    const operationLink = links.find((link) => {
      const to = link.props('to');
      return typeof to === 'object' && to !== null && 'name' in to && to.name === 'OperationCreate';
    });
    expect(operationLink).toBeDefined();
    expect(operationLink!.props('to')).toEqual({
      name: 'OperationCreate',
      params: { customerId: 'customer-1', clusterId: 'cluster-1', releaseId: 'def-apps' },
      query: {
        customerName: 'Customer 1', clusterName: 'Cluster 1', releaseName: 'apps/api', currentRevision: 7,
      },
    });

    const readonly = mount(ReleaseInventoryTable, {
      props: { releases: [release], canCreateOperation: false },
      global: { stubs: { RouterLink: RouterLinkStub } },
    });
    const readonlyLinks = readonly.findAllComponents(RouterLinkStub);
    expect(readonlyLinks.some((link) => {
      const to = link.props('to');
      return typeof to === 'object' && to !== null && 'name' in to && to.name === 'OperationCreate';
    })).toBe(false);
  });
});
