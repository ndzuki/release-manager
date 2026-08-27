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
          { releaseDefinitionId: 'definition-a', namespace: 'apps', name: 'api', chart: 'api', chartVersion: '1.0.0', revision: 1, status: 'active', valuesDigest: 'sha256:a', lastSyncAt: null, emergencyConflict: false, pendingConvergenceCount: 0, revertStatusSummary: '' },
          { releaseDefinitionId: 'definition-b', namespace: 'system', name: 'api', chart: 'api', chartVersion: '1.0.0', revision: 2, status: 'missing', valuesDigest: 'sha256:b', lastSyncAt: null, emergencyConflict: false, pendingConvergenceCount: 0, revertStatusSummary: '' },
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
      emergencyConflict: false, pendingConvergenceCount: 0, revertStatusSummary: '',
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

  it('renders the emergency entry from the single ListReleases summary (no per-row RPC, AC-058-08)', () => {
    const rows = Array.from({ length: 100 }, (_, index) => ({
      releaseDefinitionId: `def-${index}`,
      namespace: 'apps',
      name: `api-${index}`,
      chart: 'api',
      chartVersion: '1.0.0',
      revision: 1,
      status: 'active' as const,
      valuesDigest: 'sha256:a',
      lastSyncAt: null,
      emergencyConflict: false,
      pendingConvergenceCount: index % 2 === 0 ? 2 : 0,
      revertStatusSummary: '',
    }));
    const wrapper = mount(ReleaseInventoryTable, {
      props: {
        releases: rows,
        canEmergency: true,
        canConvergence: true,
        customerId: 'customer-1',
        clusterId: 'cluster-1',
      },
      global: { stubs: { RouterLink: RouterLinkStub } },
    });

    // 100 rows render their emergency info purely from summary props — the
    // component performs zero fetches by construction (no client import).
    const links = wrapper.findAllComponents(RouterLinkStub);
    const emergencyLinks = links.filter((link) => {
      const to = link.props('to');
      return typeof to === 'object' && to !== null && 'name' in to && to.name === 'EmergencyChange';
    });
    expect(emergencyLinks).toHaveLength(100);
    expect(links.filter((link) => {
      const to = link.props('to');
      return typeof to === 'object' && to !== null && 'name' in to && to.name === 'ConvergenceTasks';
    })).toHaveLength(50);
  });

  it('disables the emergency entry on conflict and explains unbound definitions (AC-058-46)', () => {
    const wrapper = mount(ReleaseInventoryTable, {
      props: {
        releases: [
          {
            releaseDefinitionId: 'def-1', namespace: 'apps', name: 'api', chart: 'api', chartVersion: '1.0.0',
            revision: 1, status: 'active' as const, valuesDigest: 'sha256:a', lastSyncAt: null,
            emergencyConflict: true, pendingConvergenceCount: 0, revertStatusSummary: '',
          },
          {
            releaseDefinitionId: '', namespace: 'apps', name: 'web', chart: 'web', chartVersion: '1.0.0',
            revision: 1, status: 'active' as const, valuesDigest: 'sha256:a', lastSyncAt: null,
            emergencyConflict: false, pendingConvergenceCount: 0, revertStatusSummary: '',
          },
        ],
        canEmergency: true,
        customerId: 'customer-1',
        clusterId: 'cluster-1',
      },
      global: { stubs: { RouterLink: RouterLinkStub } },
    });

    // Conflicted row → non-clickable span; unbound row → explanation text.
    expect(wrapper.text()).toContain('未绑定 Definition');
    const emergencyLinks = wrapper.findAllComponents(RouterLinkStub).filter((link) => {
      const to = link.props('to');
      return typeof to === 'object' && to !== null && 'name' in to && to.name === 'EmergencyChange';
    });
    expect(emergencyLinks).toHaveLength(0);
    expect(wrapper.text()).toContain('紧急变更');
  });
});
