import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import CustomerDetailPage from './CustomerDetailPage.vue';
import * as customerApi from '@/connect/customer-api';
import type * as CustomerApi from '@/connect/customer-api';
import { useAuthStore } from '@/stores/auth';

/*
 * W10 (UX-041): a successful disable used to end with only a banner flip — no
 * confirmation that the irreversible action had been applied to the named customer.
 * The failure half was already rendered (`store.disableError`); this file pins the
 * success half and that the two never appear together.
 */
vi.mock('@/connect/customer-api', async (importOriginal) => {
  const original = await importOriginal<typeof CustomerApi>();
  return {
    ...original,
    getCustomer: vi.fn(),
    listCustomerEvents: vi.fn(),
    disableCustomer: vi.fn(),
  };
});

const Stub = { template: '<div />' };

async function mountDetail() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/customers/:id', name: 'CustomerDetail', component: CustomerDetailPage },
      { path: '/customers', name: 'CustomerList', component: Stub },
      { path: '/customers/:customerId/clusters', name: 'ClusterList', component: Stub },
      { path: '/', name: 'Home', component: Stub },
    ],
  });
  await router.push('/customers/c1');
  await router.isReady();
  const wrapper = mount(CustomerDetailPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

/** The confirmation stays disabled until the impact acknowledgement is ticked. */
async function confirmDisable(wrapper: Awaited<ReturnType<typeof mountDetail>>): Promise<void> {
  await wrapper.findAll('button').find((button) => button.text() === '停用客户')?.trigger('click');
  await flushPromises();
  const panel = document.querySelector<HTMLElement>('.app-dialog__panel');
  if (!panel) throw new Error('disable dialog not rendered');
  const checkbox = panel.querySelector<HTMLInputElement>('input[type="checkbox"]');
  if (!checkbox) throw new Error('disable dialog has no acknowledgement checkbox');
  checkbox.click();
  await flushPromises();
  const confirm = Array.from(panel.querySelectorAll('button')).find(
    (button) => button.textContent?.trim() === '确认停用',
  );
  if (!confirm) throw new Error('disable dialog has no confirm button');
  confirm.click();
  await flushPromises();
}

beforeEach(() => {
  setActivePinia(createPinia());
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: {
      $typeName: 'auth.v1.SessionUser',
      id: 'admin-1',
      username: 'admin',
      roles: ['platform_admin'],
      activeOrgId: 'org-1',
    },
  });
  vi.mocked(customerApi.getCustomer).mockReset().mockResolvedValue({
    id: 'c1',
    name: 'Acme',
    slug: 'acme',
    status: 'active',
    version: 3,
  });
  vi.mocked(customerApi.listCustomerEvents).mockReset().mockResolvedValue([]);
  vi.mocked(customerApi.disableCustomer).mockReset().mockResolvedValue(undefined);
});

describe('CustomerDetailPage disable feedback (W10)', () => {
  it('announces a successful disable by name', async () => {
    const wrapper = await mountDetail();
    expect(wrapper.find('[data-testid="customer-detail-notice"]').exists()).toBe(false);

    await confirmDisable(wrapper);

    const notice = wrapper.get('[data-testid="customer-detail-notice"]');
    expect(notice.attributes('role')).toBe('status');
    expect(notice.text()).toContain('已停用客户 Acme');
    expect(customerApi.disableCustomer).toHaveBeenCalledWith('c1');
    wrapper.unmount();
  });

  it('does not claim success when the server refuses the disable', async () => {
    vi.mocked(customerApi.disableCustomer).mockRejectedValue(new Error('customer disable refused'));
    const wrapper = await mountDetail();

    await confirmDisable(wrapper);

    expect(wrapper.find('[data-testid="customer-detail-notice"]').exists()).toBe(false);
    // The refusal has its own ErrorState (rendered from store.disableError).
    expect(wrapper.text()).toContain('customer disable refused');
    wrapper.unmount();
  });
});
