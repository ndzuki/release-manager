import { mount } from '@vue/test-utils';
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
          { namespace: 'apps', name: 'api', chart: 'api', chartVersion: '1.0.0', revision: 1, status: 'active', valuesDigest: 'sha256:a', lastSyncAt: null },
          { namespace: 'system', name: 'api', chart: 'api', chartVersion: '1.0.0', revision: 2, status: 'missing', valuesDigest: 'sha256:b', lastSyncAt: null },
        ],
      },
    });

    expect(wrapper.text()).toContain('apps/api');
    expect(wrapper.text()).toContain('system/api');
  });
});
