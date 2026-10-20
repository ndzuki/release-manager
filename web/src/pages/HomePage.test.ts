import { create } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { Code, ConnectError, type Client } from '@connectrpc/connect';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory } from 'vue-router';
import HomePage from './HomePage.vue';
import { createAppRouter } from '@/router';
import { useAuthStore } from '@/stores/auth';
import { setOperationClientForTest } from '@/connect/operation-api';
import {
  resetHomeOverviewSource,
  setHomeOverviewSourceForTest,
  type HomeOverviewSource,
} from '@/components/home/homeOverviewSource';
import type { InventoryRelease } from '@/components/home/homeOverview';
import {
  ListNonTerminalOperationsResponseSchema,
  NonTerminalOperationSummarySchema,
  OrchestratorService,
  type ListNonTerminalOperationsRequest,
  type ListNonTerminalOperationsResponse,
} from '@/gen/orchestrator/v1/orchestrator_pb';

/*
 * Home overview (TASK-394).
 *
 * The page used to be a workbench grid plus a "waiting on me" list; it is now an
 * overview of four numbers, the in-progress queue and per-cluster health. These
 * cases pin three things the redesign must not lose:
 *
 *   - the entry grid is still the navigation fallback (REQ-100 §5.1 N1), and every
 *     tile names a real route;
 *   - a number only appears when the read behind it succeeded — the sync rate is
 *     derived from the inventory and reads "—" when there are no rows to divide by;
 *   - a refused or unavailable source degrades on its own, without blanking the
 *     rest of the page.
 *
 * The overview's three non-operation reads are injected through
 * `setHomeOverviewSourceForTest`; the operation feed keeps its existing client
 * seam so its rich failure states are exercised through the real composable.
 */
type FeedPage = ListNonTerminalOperationsResponse;
type FeedHandler = (request: ListNonTerminalOperationsRequest) => Promise<FeedPage>;

function summary(overrides: Record<string, unknown> = {}) {
  return create(NonTerminalOperationSummarySchema, {
    operationId: 'op-1',
    operationType: 'UPGRADE',
    state: 'running',
    releaseDefinitionId: 'def-1',
    releaseDefinitionName: 'checkout',
    customerId: 'cust-1',
    customerName: 'Acme',
    clusterId: 'cluster-1',
    createdAt: timestampFromDate(new Date('2026-10-20T10:00:00Z')),
    revision: 7,
    ...overrides,
  });
}

function page(operations: FeedPage['operations'], nextPageToken = ''): FeedPage {
  return create(ListNonTerminalOperationsResponseSchema, { operations, nextPageToken });
}

interface SourceOptions extends Partial<HomeOverviewSource> {
  probeCalls?: string[];
}

function installSource(options: SourceOptions = {}): string[] {
  const probeCalls = options.probeCalls ?? [];
  setHomeOverviewSourceForTest({
    listInventory: options.listInventory ?? (async () => []),
    listStuckLocks: options.listStuckLocks ?? (async () => 0),
    probeOperators:
      options.probeOperators ??
      (async (_customerId: string, clusterId: string) => {
        probeCalls.push(clusterId);
        return { online: 0, heartbeatSeconds: 30 };
      }),
  });
  return probeCalls;
}

function release(overrides: Partial<InventoryRelease> = {}): InventoryRelease {
  return {
    customerId: 'cust-1',
    customerName: 'Acme',
    clusterId: 'cluster-1',
    clusterName: 'prod-a',
    releaseDefinitionId: 'def-1',
    releaseDefinitionName: 'checkout',
    namespace: 'apps',
    releaseName: 'checkout',
    revision: 3,
    status: 'active',
    ...overrides,
  };
}

async function mountHome(
  options: { roles?: string[]; feed?: FeedHandler; source?: SourceOptions; organizations?: boolean } = {},
) {
  const feed = vi.fn(options.feed ?? (async () => page([])));
  setOperationClientForTest({ listNonTerminalOperations: feed } as unknown as Client<typeof OrchestratorService>);
  const probeCalls = installSource(options.source);

  const router = createAppRouter(createMemoryHistory());
  await router.push('/');
  await router.isReady();
  const wrapper = mount(HomePage, { global: { plugins: [router] } });
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: {
      $typeName: 'auth.v1.SessionUser',
      id: 'me',
      username: 'dev-admin',
      roles: options.roles ?? ['platform_admin'],
      activeOrgId: 'org-1',
    },
    organizations:
      options.organizations === false
        ? []
        : [{ $typeName: 'auth.v1.Organization', id: 'org-1', name: 'Platform Engineering' }],
  });
  for (let index = 0; index < 4; index += 1) await flushPromises();
  return { wrapper, router, feed, probeCalls };
}

function statValue(wrapper: VueWrapper, testId: string): string {
  return wrapper.get(`[data-testid="${testId}"] .stat-card__value`).text();
}

function statHint(wrapper: VueWrapper, testId: string): string {
  return wrapper.get(`[data-testid="${testId}"] .stat-card__hint`).text();
}

beforeEach(() => {
  setActivePinia(createPinia());
});

afterEach(() => {
  resetHomeOverviewSource();
  vi.unstubAllEnvs();
});

describe('HomePage workbench', () => {
  it('keeps the entry grid as the navigation fallback', async () => {
    const { wrapper } = await mountHome();

    const tiles = wrapper.findAll('[data-testid^="home-tile-"]');
    expect(tiles.length).toBeGreaterThan(5);
    expect(wrapper.find('[data-testid="home-tile-CustomerList"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="home-tile-Audit"]').exists()).toBe(true);
    // REQ-100 §5.1 N1 keeps the workbench visible, so the heading is a real h2 again.
    expect(wrapper.get('#home-quicklinks-title').text()).toBe('快速入口');
  });

  // A tile whose route name is misspelled would render and then explode on click; this
  // resolves every one of them.
  it('only links to routes that exist', async () => {
    const { wrapper, router } = await mountHome();

    for (const tile of wrapper.findAll('[data-testid^="home-tile-"]')) {
      const name = tile.attributes('data-testid')!.replace('home-tile-', '');
      expect(() => router.resolve({ name }), `${name} is not a route`).not.toThrow();
    }
  });

  it('hides capability-gated tiles from a read-only role', async () => {
    const { wrapper } = await mountHome({ roles: ['viewer'] });

    expect(wrapper.find('[data-testid="home-tile-ArtifactLifecycle"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="home-tile-Bundles"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="home-tile-LocalUsers"]').exists()).toBe(false);
    // …but the always-open entries stay.
    expect(wrapper.find('[data-testid="home-tile-CustomerList"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="home-tile-TrustPolicy"]').exists()).toBe(true);
  });
});

describe('HomePage header', () => {
  it('titles the page with the active organization', async () => {
    const { wrapper } = await mountHome();

    expect(wrapper.get('.page-header__title').text()).toBe('Platform Engineering');
    expect(wrapper.get('.page-header__subtitle').text()).toBe('发布活动与集群健康一览');
  });

  // No organization in the session payload yet: a product name beats an empty h1.
  it('falls back to the product name when the session has no organization', async () => {
    const { wrapper } = await mountHome({ organizations: false });

    expect(wrapper.get('.page-header__title').text()).toBe('Release Manager');
  });
});

describe('HomePage stat cards', () => {
  it('derives the release numbers from the organization-wide inventory', async () => {
    const { wrapper } = await mountHome({
      source: {
        listInventory: async () => [
          release({ releaseDefinitionId: 'd-1' }),
          release({ releaseDefinitionId: 'd-2', status: 'out_of_sync' }),
          release({ releaseDefinitionId: 'd-3', status: 'missing' }),
        ],
        listStuckLocks: async () => 1,
      },
    });

    expect(statValue(wrapper, 'home-stat-pending')).toBe('2');
    expect(statHint(wrapper, 'home-stat-pending')).toBe('共 3 个发布');
    expect(statValue(wrapper, 'home-stat-emergency')).toBe('1');
    expect(statHint(wrapper, 'home-stat-emergency')).toBe('需要显式收敛');
  });

  /*
   * The one number the page must never invent: there is no cross-release terminal
   * operation query, so the success rate has no sample size to divide by.
   */
  /*

   * compute; the independent review of PR #484 called the permanent "—" card a broken
   * hero. It now reports the sync rate of the inventory the page already reads, and stays
   * honest when that inventory has no rows.
   */
  it('reports the inventory sync rate and never invents a percentage', async () => {
    const { wrapper } = await mountHome({
      source: {
        listInventory: async () => [
          release({ status: 'active' }),
          release({ status: 'out_of_sync' }),
          release({ status: 'active', releaseDefinitionId: 'def-2', releaseName: 'search' }),
        ],
      },
    });

    expect(wrapper.get('[data-testid="home-stat-sync"] .stat-card__label').text()).toBe('发布同步率');
    expect(statValue(wrapper, 'home-stat-sync')).toBe('67%');
    expect(statHint(wrapper, 'home-stat-sync')).toBe('3 个发布中 2 个与集群一致');
  });

  it('shows no sync percentage when the inventory read returns nothing', async () => {
    const { wrapper } = await mountHome();
    expect(statValue(wrapper, 'home-stat-sync')).toBe('—');
    expect(statHint(wrapper, 'home-stat-sync')).toBe('当前没有发布');
  });

  it('degrades the inventory-backed cards without blanking the page when the read fails', async () => {
    const { wrapper } = await mountHome({
      feed: async () => page([summary()]),
      source: {
        listInventory: async () => {
          throw new Error('inventory down');
        },
      },
    });

    expect(statValue(wrapper, 'home-stat-pending')).toBe('—');
    expect(statHint(wrapper, 'home-stat-pending')).toBe('加载失败');
    // The operation queue has its own source and is still on screen.
    expect(wrapper.findAll('[data-testid^="home-op-"]')).toHaveLength(1);
    expect(wrapper.get('#home-operations-title').text()).toBe('进行中的操作');
  });

  it('reports a failed stuck-lock read without claiming zero emergencies', async () => {
    const { wrapper } = await mountHome({
      source: {
        listStuckLocks: async () => {
          throw new Error('stuck locks down');
        },
      },
    });

    expect(statValue(wrapper, 'home-stat-emergency')).toBe('—');
    expect(statHint(wrapper, 'home-stat-emergency')).toBe('加载失败');
  });
});

describe('HomePage live indicator', () => {
  it('sums the online operators of the probed clusters', async () => {
    const probed: string[] = [];
    const { wrapper } = await mountHome({
      source: {
        listInventory: async () => [
          release({ clusterId: 'c-a', clusterName: 'prod-a' }),
          release({ clusterId: 'c-b', clusterName: 'prod-b', releaseDefinitionId: 'd-2' }),
        ],
        probeOperators: async (_customerId, clusterId) => {
          probed.push(clusterId);
          return { online: clusterId === 'c-a' ? 1 : 2, heartbeatSeconds: 30 };
        },
      },
    });

    expect(probed).toEqual(['c-a', 'c-b']);
    expect(wrapper.get('[data-testid="home-live"]').text()).toBe('3 个 Operator 在线');
  });

  /*
   * Operators are listed per cluster, so the probe is bounded to the clusters the
   * panel renders. A count over a subset must say so rather than present itself as
   * the organization total.
   */
  it('states the coverage when not every cluster was probed', async () => {
    const probed: string[] = [];
    const { wrapper } = await mountHome({
      source: {
        listInventory: async () =>
          Array.from({ length: 8 }, (_, index) =>
            release({ clusterId: `c-${index}`, clusterName: `prod-${index}`, releaseDefinitionId: `d-${index}` }),
          ),
        probeOperators: async (_customerId, clusterId) => {
          probed.push(clusterId);
          return { online: 1, heartbeatSeconds: 30 };
        },
      },
    });

    // Six probed clusters × 1 online.
    expect(probed).toHaveLength(6);
    expect(wrapper.get('[data-testid="home-live"]').text()).toBe('6 个在线 · 已探测 6/8 个集群');
    expect(wrapper.text()).toContain('另有 2 个集群未显示');
  });

  it('says the operator state is unknown when every probe failed', async () => {
    const { wrapper } = await mountHome({
      source: {
        listInventory: async () => [release()],
        probeOperators: async () => {
          throw new Error('operators down');
        },
      },
    });

    expect(wrapper.get('[data-testid="home-live"]').text()).toBe('Operator 状态未知');
  });
});

describe('HomePage cluster health', () => {
  it('renders one row per cluster with its synced ratio and progress bar', async () => {
    const { wrapper } = await mountHome({
      source: {
        listInventory: async () => [
          release({ clusterId: 'c-a', clusterName: 'prod-a', status: 'active' }),
          release({ clusterId: 'c-a', clusterName: 'prod-a', releaseDefinitionId: 'd-2', status: 'out_of_sync' }),
          release({ clusterId: 'c-b', clusterName: 'prod-b', status: 'active' }),
        ],
        probeOperators: async (_customerId, clusterId) => ({
          online: clusterId === 'c-a' ? 2 : 0,
          heartbeatSeconds: 30,
        }),
      },
    });

    const healthy = wrapper.get('[data-testid="home-cluster-c-a"]');
    expect(healthy.text()).toContain('已同步 1/2');
    expect(healthy.text()).toContain('2 个在线');
    const bar = healthy.get('[role="progressbar"]');
    expect(bar.attributes('aria-valuenow')).toBe('1');
    expect(bar.attributes('aria-valuemax')).toBe('2');

    // A cluster with releases and no online operator is the case the design calls
    // out, so it is labelled instead of showing a bare 0.
    expect(wrapper.get('[data-testid="home-cluster-c-b"]').text()).toContain('暂无 Operator');
    expect(wrapper.text()).toContain('Operator 心跳间隔 30 秒');
  });

  it('says there is no cluster instead of rendering an empty panel', async () => {
    const { wrapper } = await mountHome();

    expect(wrapper.get('#home-clusters-title').text()).toBe('集群健康');
    expect(wrapper.text()).toContain('暂无集群');
  });
});

describe('HomePage operation panel', () => {
  const sixRows = [
    summary({ operationId: 'op-1' }),
    summary({ operationId: 'op-2' }),
    summary({ operationId: 'op-3' }),
    summary({ operationId: 'op-4' }),
    summary({ operationId: 'op-5' }),
    summary({ operationId: 'op-6' }),
  ];

  it('reads the in-progress queue from the cross-release feed', async () => {
    const { wrapper, router, feed } = await mountHome({
      feed: async () => page(sixRows, 'tok-1'),
      source: { listInventory: async () => [release()] },
    });

    // One full page is the card's count, and a next page token means "6+".
    expect(feed.mock.calls[0]![0].pageSize).toBe(100);
    expect(statValue(wrapper, 'home-stat-operations')).toBe('6+');
    // …every row is `running`, so all six count as dispatched.
    expect(statHint(wrapper, 'home-stat-operations')).toBe('6 个在下发阶段');

    const rows = wrapper.findAll('[data-testid^="home-op-"]');
    expect(rows).toHaveLength(5);
    expect(rows[0]!.text()).toContain('checkout');
    expect(rows[0]!.text()).toContain('prod-a');
    expect(rows[0]!.text()).toContain('执行中');

    // Every row reaches the detail through a route the app defines, and the panel
    // links to the centre itself.
    expect(router.resolve(rows[0]!.attributes('href')!).name).toBe('OperationCenterDetail');
    expect(wrapper.get('.home-panel__link').attributes('href')).toBe('/operations');
    expect(wrapper.text()).toContain('已加载 6 条，可能还有更多');
  });

  it('does not add "+" when the whole queue fits on one page', async () => {
    const { wrapper } = await mountHome({ roles: ['viewer'], feed: async () => page([summary(), summary({ operationId: 'op-2', state: 'pending' })]) });

    expect(statValue(wrapper, 'home-stat-operations')).toBe('2');
    // Only the running one is already dispatched.
    expect(statHint(wrapper, 'home-stat-operations')).toBe('1 个在下发阶段');
    expect(wrapper.findAll('[data-testid^="home-op-"]')).toHaveLength(2);
    expect(wrapper.text()).toContain('按等待时长升序');
  });

  it('says nothing is waiting instead of showing an empty list', async () => {
    const { wrapper } = await mountHome({ feed: async () => page([]) });

    expect(wrapper.text()).toContain('当前没有待处理的操作。');
    expect(wrapper.find('.home-panel__rows').exists()).toBe(false);
  });

  it('degrades to a maintenance notice without hiding the release numbers', async () => {
    const { wrapper } = await mountHome({
      feed: async () => {
        throw new ConnectError('maintenance', Code.Unavailable);
      },
      source: {
        listInventory: async () => [release({ status: 'out_of_sync' })],
      },
    });

    expect(wrapper.text()).toContain('维护中不可用');
    expect(statValue(wrapper, 'home-stat-pending')).toBe('1');
    // The queue panel degrades to a status notice, not an assertive alert: nothing here is
    // a new failure the user must act on (the operator is told to come back later).
    expect(wrapper.find('.home-page__columns [role="alert"]').exists()).toBe(false);
  });

  it('offers a retry with the failure copy when the queue read fails', async () => {
    let attempts = 0;
    const { wrapper } = await mountHome({
      feed: async () => {
        attempts += 1;
        if (attempts === 1) throw new ConnectError('backend down', Code.Unavailable);
        return page([summary()]);
      },
    });

    expect(wrapper.text()).toContain('待办加载失败');
    await wrapper.get('.home-panel__state[role="alert"] button').trigger('click');
    for (let index = 0; index < 4; index += 1) await flushPromises();

    expect(attempts).toBe(2);
    expect(wrapper.findAll('[data-testid^="home-op-"]')).toHaveLength(1);
  });

  it('keeps the panel out of the page while the operations feature is off', async () => {
    vi.stubEnv('VITE_ENABLE_RELEASE_OPERATIONS', 'false');
    const { wrapper, feed } = await mountHome();

    expect(wrapper.find('#home-operations-title').exists()).toBe(false);
    expect(statHint(wrapper, 'home-stat-operations')).toBe('功能未启用');
    expect(feed).not.toHaveBeenCalled();
  });
});

describe('HomePage create operation', () => {
  it('hides the create action from a role that cannot create operations', async () => {
    const { wrapper } = await mountHome({
      roles: ['viewer'],
      source: { listInventory: async () => [release()] },
    });

    expect(wrapper.find('[data-testid="home-create"]').exists()).toBe(false);
  });

  it('hides the create action when the inventory has no release to target', async () => {
    const { wrapper } = await mountHome();

    expect(wrapper.find('[data-testid="home-create"]').exists()).toBe(false);
  });

  /*
   * The create route needs a release as its context. Rather than sending the reader
   * to a picker they must fill themselves, the dialog offers the releases this page
   * already read — the same inventory the numbers above it are computed from.
   */
  it('navigates to the operation form with the selected release', async () => {
    const { wrapper, router } = await mountHome({
      source: {
        listInventory: async () => [
          release({ releaseName: 'checkout', releaseDefinitionName: 'checkout' }),
          release({
            clusterId: 'cluster-2',
            clusterName: 'prod-b',
            releaseDefinitionId: 'def-2',
            releaseName: 'billing',
            releaseDefinitionName: 'billing',
            revision: 9,
          }),
        ],
      },
    });

    expect(router.hasRoute('OperationCreate')).toBe(true);
    await wrapper.get('[data-testid="home-create"]').trigger('click');
    await flushPromises();

    const select = document.querySelector<HTMLSelectElement>('#home-create-release');
    expect(select).not.toBeNull();
    expect(select!.options).toHaveLength(2);

    // Ordered by release name, so index 0 is the "billing" release.
    select!.value = '0';
    select!.dispatchEvent(new Event('change'));
    await flushPromises();

    const confirm = document.querySelector<HTMLButtonElement>('[data-testid="home-create-confirm"]');
    expect(confirm).not.toBeNull();
    confirm!.click();

    // The create page is lazily imported, so the navigation settles after the
    // click's microtasks; wait for the route rather than for a fixed tick count.
    await vi.waitFor(() => {
      expect(router.currentRoute.value.name).toBe('OperationCreate');
    });
    expect(router.currentRoute.value.params).toMatchObject({
      customerId: 'cust-1',
      clusterId: 'cluster-2',
      releaseId: 'def-2',
    });
    expect(router.currentRoute.value.query.currentRevision).toBe('9');
  });
});
