import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory, createRouter } from 'vue-router';
import ReleaseInventoryPage from './ReleaseInventoryPage.vue';
import { getAuthorizationSnapshot } from '@/connect/emergency-api';
import type * as EmergencyApi from '@/connect/emergency-api';
import { orchestratorClient } from '@/connect/client';
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
      // TASK-267: the sync-accepted notice needs a sync entry that answers; the
      // per-case value is overridden in the notice-lifetime suite below.
      triggerInventorySync: vi.fn(async () => ({
        $typeName: 'orchestrator.v1.TriggerInventorySyncResponse',
        syncRequestId: 'sync-1',
      })),
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
    expect(wrapper.get('.eyebrow').text()).toBe('发布清单');
  });

  it('renders no stale notice once the snapshot is fresh', async () => {
    vi.mocked(getAuthorizationSnapshot).mockResolvedValue(snapshot(true));

    const wrapper = await mountPage();
    await vi.waitFor(() => expect(getAuthorizationSnapshot).toHaveBeenCalled());

    expect(wrapper.find('[role="status"]').exists()).toBe(false);
  });
});

/*
 * TASK-267. The sync-accepted line is the shared Toast primitive now. Toast hides
 * itself on dismiss and never turns that flag back on, so a second `syncRequestId`
 * handed to the SAME instance renders nothing (the single-instance limit pinned in
 * Toast.test.ts). `triggerSync` (stores/releaseInventory.ts) does not clear the id
 * before asking again — it only assigns the new one on success — so the page's
 * `@dismiss="inventory.syncRequestId = null"` is the only thing that drops the
 * `v-if` and remounts the primitive. Hence a page-level sequence, not just
 * "the box disappears": sync accepted -> dismiss -> sync accepted again.
 */
describe('ReleaseInventoryPage sync notice lifetime (TASK-267)', () => {
  it('shows the sync notice again after it is dismissed and the next sync is accepted', async () => {
    vi.mocked(getAuthorizationSnapshot).mockResolvedValue(snapshot(true));
    const requested = vi.mocked(orchestratorClient.triggerInventorySync);
    requested.mockReset().mockResolvedValue({
      $typeName: 'orchestrator.v1.TriggerInventorySyncResponse',
      syncRequestId: 'sync-1',
    });

    const wrapper = await mountPage();
    await flushPromises();

    const syncEntry = wrapper.findAll('button').find((button) => button.text() === '触发同步');
    if (!syncEntry) throw new Error('sync entry not rendered');

    await syncEntry.trigger('click');
    await flushPromises();
    expect(wrapper.get('[data-testid="inventory-sync-notice"]').text()).toContain('sync-1');

    await wrapper.get('[data-testid="inventory-sync-notice-dismiss"]').trigger('click');
    expect(wrapper.find('[data-testid="inventory-sync-notice"]').exists()).toBe(false);

    requested.mockResolvedValue({
      $typeName: 'orchestrator.v1.TriggerInventorySyncResponse',
      syncRequestId: 'sync-2',
    });
    await syncEntry.trigger('click');
    await flushPromises();
    const notice = wrapper.get('[data-testid="inventory-sync-notice"]');
    expect(notice.text()).toContain('sync-2');
  });
});
