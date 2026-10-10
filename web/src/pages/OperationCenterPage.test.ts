import { create } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { Code, ConnectError, type Client } from '@connectrpc/connect';
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils';
import { createPinia, setActivePinia } from 'pinia';
import { createMemoryHistory } from 'vue-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  ListNonTerminalOperationsResponseSchema,
  NonTerminalOperationSummarySchema,
  OrchestratorService,
  type ListNonTerminalOperationsRequest,
  type ListNonTerminalOperationsResponse,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { setOperationClientForTest } from '@/connect/operation-api';
import { createAppRouter } from '@/router';
import OperationCenterPage from './OperationCenterPage.vue';

/*
 * The Operation centre's contract (TASK-277 AC-02/AC-04).
 *
 * The page must never guess: the queue is paged by an OPAQUE cursor, so "next" is only
 * offered while the server sent next_page_token and the cursor of the page being left is
 * the one sent back. Three of the four degradations here are NOT the shared error state —
 * maintenance and an out-of-scope refusal must not offer a pointless retry — so each gets
 * its own assertion.
 */
type FeedPage = ListNonTerminalOperationsResponse;

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
    updatedAt: timestampFromDate(new Date('2026-10-01T00:05:00Z')),
    revision: 7,
    ...overrides,
  });
}

function page(operations: FeedPage['operations'], nextPageToken = ''): FeedPage {
  return create(ListNonTerminalOperationsResponseSchema, { operations, nextPageToken });
}

/** A client whose every call is recorded, so the cursor actually sent can be asserted. */
function feedClient(handler: (request: ListNonTerminalOperationsRequest) => Promise<FeedPage>) {
  const mock = vi.fn(handler);
  setOperationClientForTest({ listNonTerminalOperations: mock } as unknown as Client<typeof OrchestratorService>);
  return mock;
}

async function mountCenter(): Promise<VueWrapper> {
  const router = createAppRouter(createMemoryHistory(), true, true, true);
  await router.push('/operations');
  await router.isReady();
  const wrapper = mount(OperationCenterPage, { global: { plugins: [router] } });
  await flushPromises();
  return wrapper;
}

function nextButton(wrapper: VueWrapper) {
  return wrapper.findAll('.operation-center__pager button')[1]!;
}

function prevButton(wrapper: VueWrapper) {
  return wrapper.findAll('.operation-center__pager button')[0]!;
}

beforeEach(() => {
  setActivePinia(createPinia());
});

describe('OperationCenterPage states', () => {
  it('renders the loading state before any page arrives', async () => {
    let resolvePage: (value: FeedPage) => void = () => {};
    feedClient(() => new Promise<FeedPage>((resolve) => { resolvePage = resolve; }));

    const wrapper = await mountCenter();

    expect(wrapper.find('[role="status"]').attributes('aria-label')).toBe('加载中');
    expect(wrapper.find('table').exists()).toBe(false);

    resolvePage(page([summary()]));
    await flushPromises();
    expect(wrapper.find('table').exists()).toBe(true);
  });

  it('renders the empty state with the operation-centre copy', async () => {
    feedClient(async () => page([]));

    const wrapper = await mountCenter();

    expect(wrapper.get('.empty-state').text()).toContain('当前作用域内没有非终态的 Operation。');
    expect(wrapper.find('table').exists()).toBe(false);
    // The cursor is the only end-of-list signal, so an empty first page has no pager.
    expect(wrapper.find('.operation-center__pager').exists()).toBe(false);
  });

  it('renders the rows with their identities, a detail link and the emergency badge', async () => {
    const mock = feedClient(async () =>
      page([
        summary({ operationId: 'op-1' }),
        summary({
          operationId: 'op-2',
          operationType: 'EMERGENCY',
          state: 'queued',
          releaseDefinitionName: '',
          releaseDefinitionId: 'def-2',
          customerName: '',
          customerId: 'cust-2',
          emergency: true,
        }),
      ]),
    );

    const wrapper = await mountCenter();

    const rows = wrapper.findAll('tbody tr');
    expect(rows).toHaveLength(2);
    expect(rows[0]!.text()).toContain('Acme');
    expect(rows[0]!.text()).toContain('checkout');
    expect(rows[0]!.text()).toContain('执行中');
    // An empty display name falls back to the id rather than rendering a blank cell.
    expect(rows[1]!.text()).toContain('def-2');
    expect(rows[1]!.text()).toContain('cust-2');
    expect(rows[1]!.text()).toContain('紧急');
    expect(rows[0]!.get('a').attributes('href')).toContain('/operations/op-1');
    expect(rows[0]!.get('a').attributes('href')).toContain('releaseName=checkout');
    // The centre's page size is part of the contract the server clamps.
    expect(mock.mock.calls[0]![0].pageSize).toBe(20);
  });

  it('renders a retryable error state when the read fails for another reason', async () => {
    const mock = feedClient(async () => {
      throw new ConnectError('backend down', Code.Unavailable);
    });

    const wrapper = await mountCenter();

    expect(wrapper.get('[role="alert"]').text()).toContain('网络错误，请检查连接后重试');
    expect(wrapper.find('table').exists()).toBe(false);

    await wrapper.get('[role="alert"] button').trigger('click');
    await flushPromises();
    expect(mock).toHaveBeenCalledTimes(2);
  });
});

describe('OperationCenterPage degradations', () => {
  it('degrades to a maintenance notice instead of an error, a blank page or a spinner', async () => {
    feedClient(async () => {
      throw new ConnectError('maintenance', Code.Unavailable);
    });

    const wrapper = await mountCenter();

    expect(wrapper.get('[data-testid="operation-center-maintenance"]').text()).toContain(
      '维护中不可用：平台正处于维护模式',
    );
    // The copy above and useOperationFeed's comment both say no retry can work until the
    // cutover ends, and the home panel offers none either — so this block must not either.
    expect(wrapper.find('[data-testid="operation-center-maintenance"] button').exists()).toBe(false);
    expect(wrapper.findAll('button')).toHaveLength(0);
    expect(wrapper.find('[role="alert"]').exists()).toBe(false);
    // Not an endless spinner either: the degradation is terminal until the cutover ends.
    expect(wrapper.find('.loading-state').exists()).toBe(false);
    expect(wrapper.find('table').exists()).toBe(false);
    expect(wrapper.find('.operation-center__pager').exists()).toBe(false);
  });

  it('degrades to the forbidden state when the read is refused', async () => {
    feedClient(async () => {
      throw new ConnectError('release:read denied', Code.PermissionDenied);
    });

    const wrapper = await mountCenter();

    // This UI never sends a customerId, so the refusal is the authorization decision on
    // the read (casbin release:read, or an inactive membership) — not a customer scope.
    expect(wrapper.get('.forbidden-state').text()).toContain('无读取权限：当前账号无权读取该作用域内的 Operation。');
    expect(wrapper.find('table').exists()).toBe(false);
    // A refusal is not retryable: repeating the same scope cannot succeed.
    expect(wrapper.find('[role="alert"] button').exists()).toBe(false);
  });
});

describe('OperationCenterPage pagination', () => {
  it('offers the next page only while the server sent a token, and sends that token back', async () => {
    const mock = feedClient(async (request) =>
      request.pageToken === ''
        ? page([summary({ operationId: 'op-1' })], 'tok-1')
        : page([summary({ operationId: 'op-2' })], ''),
    );

    const wrapper = await mountCenter();

    expect(prevButton(wrapper).attributes('disabled')).toBeDefined();
    expect(nextButton(wrapper).attributes('disabled')).toBeUndefined();
    expect(wrapper.get('.operation-center__page').text()).toBe('第 1 页');

    await nextButton(wrapper).trigger('click');
    await flushPromises();

    // The cursor of the page being LEFT is what continues the walk.
    expect(mock.mock.calls[1]![0].pageToken).toBe('tok-1');
    expect(wrapper.findAll('tbody tr')).toHaveLength(1);
    expect(wrapper.text()).toContain('op-2');
    expect(wrapper.get('.operation-center__page').text()).toBe('第 2 页');
    // The last page has no token, so "next" is disabled — not hidden, so the keyboard
    // order does not shift under the user.
    expect(nextButton(wrapper).attributes('disabled')).toBeDefined();
    expect(prevButton(wrapper).attributes('disabled')).toBeUndefined();

    await prevButton(wrapper).trigger('click');
    await flushPromises();

    // Going back replays the cursor that produced the first page.
    expect(mock.mock.calls[2]![0].pageToken).toBe('');
    expect(wrapper.text()).toContain('op-1');
    expect(wrapper.get('.operation-center__page').text()).toBe('第 1 页');
  });

  /*
   * The cursor stack IS the paging contract, and a two-page round trip cannot see it:
   * from page 2, "previous" replays the empty first cursor whether or not the stack was
   * ever recorded. Three pages make each stored cursor observable, so this test asserts
   * the exact cursor sequence that reached the wire — request by request, not just the
   * rows that happened to render.
   */
  it('walks a three-page cursor stack back and forward, sending each stored cursor', async () => {
    const pages: Record<string, FeedPage> = {
      '': page([summary({ operationId: 'op-1' })], 't1'),
      t1: page([summary({ operationId: 'op-2' })], 't2'),
      t2: page([summary({ operationId: 'op-3' })], ''),
    };
    const mock = feedClient(async (request) => pages[request.pageToken] ?? page([]));

    const wrapper = await mountCenter();
    const goNextOnce = async () => {
      await nextButton(wrapper).trigger('click');
      await flushPromises();
    };
    const goPrevOnce = async () => {
      await prevButton(wrapper).trigger('click');
      await flushPromises();
    };
    const firstRow = () => wrapper.get('tbody tr').text();

    expect(firstRow()).toContain('op-1');

    await goNextOnce(); // page 2: the stack records t1 for position 1
    expect(firstRow()).toContain('op-2');
    await goNextOnce(); // page 3: the stack records t2 for position 2
    expect(firstRow()).toContain('op-3');
    expect(wrapper.get('.operation-center__page').text()).toBe('第 3 页');

    await goPrevOnce(); // back to page 2 by REPLAYING cursors[1] = t1
    expect(firstRow()).toContain('op-2');
    await goPrevOnce(); // back to page 1 by REPLAYING cursors[0] = ''
    expect(firstRow()).toContain('op-1');
    expect(wrapper.get('.operation-center__page').text()).toBe('第 1 页');

    await goNextOnce(); // forward again re-enters position 1 with its recorded cursor
    expect(firstRow()).toContain('op-2');
    await goNextOnce();
    expect(firstRow()).toContain('op-3');

    // '' → t1 → t2 (forward), then t1 → '' (back, replaying the stack), then t1 → t2.
    expect(mock.mock.calls.map((call) => call[0].pageToken)).toEqual([
      '', 't1', 't2', 't1', '', 't1', 't2',
    ]);
  });

  it('disables the next page on a single-page result', async () => {
    feedClient(async () => page([summary()], ''));

    const wrapper = await mountCenter();

    expect(nextButton(wrapper).attributes('disabled')).toBeDefined();
    expect(prevButton(wrapper).attributes('disabled')).toBeDefined();
  });
});

describe('OperationCenterPage accessibility', () => {
  it('keeps the table primitive\u2019s sortable header, aria-sort and native button', async () => {
    feedClient(async () => page([summary()]));

    const wrapper = await mountCenter();
    const header = wrapper.get('th[data-key="createdAt"]');

    expect(header.get('button').element.tagName).toBe('BUTTON');
    expect(header.attributes('aria-sort')).toBe('none');

    await header.get('button').trigger('click');

    // aria-sort is the only part of the sort state a screen reader receives.
    expect(header.attributes('aria-sort')).toBe('ascending');
    expect(wrapper.get('th[data-key="revision"]').attributes('aria-sort')).toBe('none');
    expect(wrapper.find('th[data-key="state"] button').exists()).toBe(false);
  });

  it('labels the pager region', async () => {
    feedClient(async () => page([summary()], 'tok-1'));

    const wrapper = await mountCenter();

    expect(wrapper.get('.operation-center__pager').attributes('aria-label')).toBe('Operation 分页');
  });
});
