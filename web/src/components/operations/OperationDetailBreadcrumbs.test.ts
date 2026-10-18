import { mount } from '@vue/test-utils';
import { createMemoryHistory, createRouter } from 'vue-router';
import { beforeEach, describe, expect, it } from 'vitest';
import { t } from '@/i18n/messages';
import OperationDetailBreadcrumbs from './OperationDetailBreadcrumbs.vue';

/*
 * The breadcrumb trail anchors on the release context when the page has one and on
 * the Operation centre when it does not (TASK-277/279). Both halves are observable
 * here: the hrefs must be real (no links built from absent params) and the trail must
 * stay on the page's own root class.
 */

const router = createRouter({
  history: createMemoryHistory(),
  routes: [
    {
      path: '/customers/:customerId/clusters/:clusterId/releases',
      name: 'ReleaseInventory',
      component: { template: '<div />' },
    },
    {
      path: '/customers/:customerId/clusters/:clusterId/releases/:releaseId/operations',
      name: 'OperationList',
      component: { template: '<div />' },
    },
    { path: '/operations', name: 'OperationCenter', component: { template: '<div />' } },
  ],
});

beforeEach(async () => {
  await router.push('/operations');
  await router.isReady();
});

describe('OperationDetailBreadcrumbs', () => {
  it('links the release-scoped trail when the page holds a release context', () => {
    const wrapper = mount(OperationDetailBreadcrumbs, {
      props: {
        releaseScope: { customerId: 'cust-1', clusterId: 'cluster-1', releaseId: 'def-1' },
        releaseName: 'checkout',
        operationId: 'op-1',
      },
      global: { plugins: [router] },
    });

    const nav = wrapper.get('nav.operation-detail__breadcrumbs');
    expect(nav.attributes('aria-label')).toBe(t('values.page.breadcrumb'));
    expect(wrapper.get('a[href="/customers/cust-1/clusters/cluster-1/releases"]').text()).toBe('Releases');
    expect(
      wrapper.get('a[href="/customers/cust-1/clusters/cluster-1/releases/def-1/operations"]').text(),
    ).toBe('操作历史');
    expect(nav.text()).toContain('checkout');
    expect(nav.get('strong').text()).toBe('op-1');
  });

  it('anchors on the Operation centre when there is no release context', () => {
    const wrapper = mount(OperationDetailBreadcrumbs, {
      props: { releaseScope: null, releaseName: 'checkout', operationId: 'op-center' },
      global: { plugins: [router] },
    });

    const nav = wrapper.get('nav.operation-detail__breadcrumbs');
    expect(wrapper.get('a[href="/operations"]').text()).toBe(t('operationCenter.title'));
    // The release-scoped links are absent, not broken.
    expect(nav.text()).not.toContain('Releases');
    expect(wrapper.find('a[href^="/customers/"]').exists()).toBe(false);
  });
});
