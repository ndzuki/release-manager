import { mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import ReleaseInventoryPage from './ReleaseInventoryPage.vue';
import { getAuthorizationSnapshot } from '@/connect/emergency-api';
import type * as EmergencyApi from '@/connect/emergency-api';
import type * as Client from '@/connect/client';
import { useAuthStore } from '@/stores/auth';

vi.mock('@/connect/emergency-api', async (importOriginal) => {
  const original = await importOriginal<typeof EmergencyApi>();
  return { ...original, getAuthorizationSnapshot: vi.fn() };
});

// The page renders the table only once the inventory load settles; an unmocked
// orchestrator client would leave it in the error branch and hide the entry we
// are asserting on. Everything else is kept real so the other connect modules
// still see their clients.
vi.mock('@/connect/client', async (importOriginal) => {
  const actual = await importOriginal<typeof Client>();
  return {
    ...actual,
    orchestratorClient: {
      ...actual.orchestratorClient,
      listReleases: vi.fn(async () => ({ releases: [], totalCount: 0, nextCursor: '' })),
    },
  };
});

function snapshot(fresh: boolean) {
  return {
    organizationId: 'org-1',
    customerId: 'cust-1',
    bindingActive: true,
    customerActive: true,
    role: 'release_admin',
    canExecuteEmergency: true,
    canResolveEmergency: true,
    canCreateValuesRevision: true,
    canApproveValuesRevision: true,
    sourceVersion: 7n,
    policyVersion: 1n,
    checkpoint: fresh ? 7n : 0n,
    fresh,
    actorId: 'user-1',
    emergencyChangeEnabled: true,
  };
}

async function mountPage() {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/customers/:customerId/clusters/:clusterId/releases', name: 'ReleaseInventory', component: ReleaseInventoryPage },
      { path: '/:pathMatch(.*)*', name: 'Elsewhere', component: { template: '<div />' } },
    ],
  });
  await router.push('/customers/cust-1/clusters/cls-1/releases');
  await router.isReady();
  return mount(ReleaseInventoryPage, { global: { plugins: [router] } });
}

beforeEach(() => {
  setActivePinia(createPinia());
  vi.mocked(getAuthorizationSnapshot).mockReset();
  useAuthStore().$patch({
    user: { $typeName: 'auth.v1.SessionUser', id: 'user-1', username: 'admin', roles: ['release_admin'], activeOrgId: 'org-1' },
    // activeOrganization is derived by matching user.activeOrgId against this
    // list, and the page only loads a snapshot when an organization resolves.
    organizations: [{ id: 'org-1', name: 'Org One' }],
  });
});

describe('ReleaseInventoryPage authorization freshness (REQ-033 AC-033-10)', () => {
  // The creation entry itself lives in ReleaseInventoryTable, which only renders
  // once the inventory is non-empty; this suite therefore asserts the page-level
  // signal (the notice and the gate that feeds the table), while the store-level
  // writeAllowed contract is covered by emergencyAuthorization.test.ts.
  it('surfaces the stale state while the snapshot is not fresh', async () => {
    vi.mocked(getAuthorizationSnapshot).mockResolvedValue(snapshot(false));

    const wrapper = await mountPage();
    await vi.waitFor(() => expect(getAuthorizationSnapshot).toHaveBeenCalled());

    const notice = wrapper.get('[role="status"]');
    expect(notice.text()).toContain('授权数据未同步');
    // The inventory itself stays readable — only the write entry closes.
    expect(wrapper.text()).toContain('Release inventory');
  });

  it('renders no stale notice once the snapshot is fresh', async () => {
    vi.mocked(getAuthorizationSnapshot).mockResolvedValue(snapshot(true));

    const wrapper = await mountPage();
    await vi.waitFor(() => expect(getAuthorizationSnapshot).toHaveBeenCalled());

    expect(wrapper.find('[role="status"]').exists()).toBe(false);
  });
});
