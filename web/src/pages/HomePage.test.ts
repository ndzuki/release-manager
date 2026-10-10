import { create } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { Code, ConnectError, type Client } from '@connectrpc/connect';
import { flushPromises, mount } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createMemoryHistory } from 'vue-router';
import HomePage from './HomePage.vue';
import { createAppRouter } from '@/router';
import { useAuthStore } from '@/stores/auth';
import { setOperationClientForTest } from '@/connect/operation-api';
import {
  ListNonTerminalOperationsResponseSchema,
  NonTerminalOperationSummarySchema,
  OrchestratorService,
  type ListNonTerminalOperationsRequest,
  type ListNonTerminalOperationsResponse,
} from '@/gen/orchestrator/v1/orchestrator_pb';

/*
 * Plan N1: the home page was a dead end (one welcome empty state, a Dismiss button and
 * zero RouterLinks). These cases pin the workbench and — more usefully — that every
 * tile names a route the app actually defines.
 *
 * TASK-277 adds the "waiting on me" panel, whose data source is the cross-release
 * aggregate; the page used to carry a comment saying no such source existed. Its
 * count is probed (page size = limit + 1), so it must read "N+" as soon as a next page
 * exists rather than claim a total the API never returns.
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
    createdAt: timestampFromDate(new Date('2026-10-01T00:00:00Z')),
    revision: 7,
    ...overrides,
  });
}

function page(operations: FeedPage['operations'], nextPageToken = ''): FeedPage {
  return create(ListNonTerminalOperationsResponseSchema, { operations, nextPageToken });
}

function stubFeed(handler: FeedHandler) {
  const mock = vi.fn(handler);
  setOperationClientForTest({ listNonTerminalOperations: mock } as unknown as Client<typeof OrchestratorService>);
  return mock;
}

async function mountHome(roles: string[], handler: FeedHandler = async () => page([])) {
  const feed = stubFeed(handler);
  const router = createAppRouter(createMemoryHistory());
  await router.push('/');
  await router.isReady();
  const wrapper = mount(HomePage, { global: { plugins: [router] } });
  useAuthStore().$patch({
    status: 'authenticated',
    initialized: true,
    user: { $typeName: 'auth.v1.SessionUser', id: 'me', username: 'dev-admin', roles, activeOrgId: 'org-1' },
  });
  await flushPromises();
  await flushPromises();
  return { wrapper, router, feed };
}

beforeEach(() => {
  setActivePinia(createPinia());
});

describe('HomePage workbench', () => {
  it('offers real destinations instead of a dead end', async () => {
    const { wrapper } = await mountHome(['platform_admin']);

    const tiles = wrapper.findAll('[data-testid^="home-tile-"]');
    expect(tiles.length).toBeGreaterThan(5);
    expect(wrapper.find('[data-testid="home-tile-CustomerList"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="home-tile-Audit"]').exists()).toBe(true);
  });

  // A tile whose route name is misspelled would render and then explode on click; this
  // resolves every one of them.
  it('only links to routes that exist', async () => {
    const { wrapper, router } = await mountHome(['platform_admin']);

    for (const tile of wrapper.findAll('[data-testid^="home-tile-"]')) {
      const name = tile.attributes('data-testid')!.replace('home-tile-', '');
      expect(() => router.resolve({ name }), `${name} is not a route`).not.toThrow();
    }
  });

  it('hides capability-gated tiles from a read-only role', async () => {
    const { wrapper } = await mountHome(['viewer']);

    expect(wrapper.find('[data-testid="home-tile-ArtifactLifecycle"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="home-tile-Bundles"]').exists()).toBe(false);
    expect(wrapper.find('[data-testid="home-tile-LocalUsers"]').exists()).toBe(false);
    // …but the always-open entries stay.
    expect(wrapper.find('[data-testid="home-tile-CustomerList"]').exists()).toBe(true);
    expect(wrapper.find('[data-testid="home-tile-TrustPolicy"]').exists()).toBe(true);
  });
});

describe('HomePage pending work', () => {
  const sixRows = [
    summary({ operationId: 'op-1' }),
    summary({ operationId: 'op-2' }),
    summary({ operationId: 'op-3' }),
    summary({ operationId: 'op-4' }),
    summary({ operationId: 'op-5' }),
    summary({ operationId: 'op-6' }),
  ];

  it('shows the count plus the first rows, from the cross-release aggregate', async () => {
    const { wrapper, router, feed } = await mountHome(['platform_admin'], async () => page(sixRows));

    // The probe row proves there is more without loading a second page…
    expect(feed.mock.calls[0]![0].pageSize).toBe(6);
    // …and the count says "5+" rather than inventing a total the RPC never returns.
    expect(wrapper.get('.home-page__todo-count').text()).toBe('待我处理 5+ 条');

    const rows = wrapper.findAll('[data-testid^="home-todo-"]');
    expect(rows).toHaveLength(5);
    expect(rows[0]!.text()).toContain('checkout');
    expect(rows[0]!.text()).toContain('Acme');
    expect(rows[0]!.text()).toContain('执行中');

    // Every row reaches the detail through a route the app defines, and the panel links
    // to the centre itself.
    const href = rows[0]!.attributes('href')!;
    expect(router.resolve(href).name).toBe('OperationCenterDetail');
    expect(wrapper.get('.home-page__todo-header a').attributes('href')).toBe('/operations');
  });

  it('does not add "+" when the whole queue fits on one page', async () => {
    const { wrapper } = await mountHome(['viewer'], async () => page([summary(), summary({ operationId: 'op-2' })]));

    expect(wrapper.get('.home-page__todo-count').text()).toBe('待我处理 2 条');
    expect(wrapper.findAll('[data-testid^="home-todo-"]')).toHaveLength(2);
  });

  it('says nothing is waiting instead of showing an empty list', async () => {
    const { wrapper } = await mountHome(['viewer'], async () => page([], ''));

    expect(wrapper.get('.home-page__todo-note').text()).toBe('当前没有待处理的 Operation。');
    expect(wrapper.find('.home-page__todo-list').exists()).toBe(false);
  });

  it('degrades to a maintenance notice when the aggregate is refused during a cutover', async () => {
    const { wrapper } = await mountHome(['viewer'], async () => {
      throw new ConnectError('maintenance', Code.Unavailable);
    });

    expect(wrapper.get('.home-page__todo-note').text()).toBe('维护中不可用');
    expect(wrapper.find('.home-page__todo-note[role="alert"]').exists()).toBe(false);
    expect(wrapper.find('.home-page__todo-list').exists()).toBe(false);
  });

  it('offers a retry with the failure copy when the read fails', async () => {
    const { wrapper, feed } = await mountHome(['viewer'], async () => {
      throw new ConnectError('backend down', Code.Unavailable);
    });

    const note = wrapper.get('.home-page__todo-note[role="alert"]');
    expect(note.text()).toContain('待办加载失败');

    await note.get('button').trigger('click');
    await flushPromises();
    await flushPromises();

    expect(feed).toHaveBeenCalledTimes(2);
  });

  it('keeps the panel out of the page while the operations feature is off', async () => {
    vi.stubEnv('VITE_ENABLE_RELEASE_OPERATIONS', 'false');
    try {
      const { wrapper, feed } = await mountHome(['viewer']);

      expect(wrapper.find('.home-page__todo').exists()).toBe(false);
      expect(feed).not.toHaveBeenCalled();
    } finally {
      vi.unstubAllEnvs();
    }
  });
});
