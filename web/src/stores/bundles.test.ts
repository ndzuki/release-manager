import { createPinia, setActivePinia } from 'pinia';
import { Code, ConnectError } from '@connectrpc/connect';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useBundlesStore } from './bundles';
import * as api from '@/connect/bundle-api';
import { BundleStatus } from '@/gen/common/v1/domain_pb';

vi.mock('@/connect/bundle-api', async (importOriginal) => {
  const original = await importOriginal<typeof api>();
  return { ...original, listBundles: vi.fn(), getBundle: vi.fn() };
});

const mockedList = vi.mocked(api.listBundles);
const mockedDetail = vi.mocked(api.getBundle);

function summary(id: string): api.BundleSummaryView {
  return {
    id,
    name: `bundle-${id}`,
    digest: 'sha256:abc',
    status: BundleStatus.VALIDATED,
    statusLabel: '已校验',
    chartRef: 'chart',
    chartVersion: '1.0.0',
    chartDigest: 'sha256:def',
    images: [],
    createdAt: '2026-09-28T00:00:00Z',
  };
}

function page(ids: string[], nextPageToken = '', totalSize = ids.length): api.BundlePage {
  return { bundles: ids.map(summary), nextPageToken, totalSize };
}

beforeEach(() => {
  setActivePinia(createPinia());
  mockedList.mockReset().mockResolvedValue(page(['b1'], '', 1));
  mockedDetail.mockReset().mockResolvedValue({ ...summary('b1'), gitCommit: 'abc123', pipelineId: 'p-1', signatureDigest: 's', sbomDigest: 'sb', provenanceDigest: 'pr', signatureRef: 'ref-s', sbomRef: 'ref-sb', provenanceRef: 'ref-pr' });
});

describe('bundles store', () => {
  // The server requires the definition (scope + authorization), so the store must not
  // spend a request to learn that — and must not let a 403 render as an empty list.
  it('does not call the server until a release definition is given', async () => {
    const store = useBundlesStore();

    await store.load({ releaseDefinitionId: '', chartNameFilter: '' });

    expect(mockedList).not.toHaveBeenCalled();
    expect(store.needsDefinition).toBe(true);
    expect(store.failure).toBeNull();
    expect(store.isEmpty).toBe(false);
  });

  it('loads the first page with the current filters and reports the total', async () => {
    const store = useBundlesStore();

    await store.load({ releaseDefinitionId: 'def-1', status: BundleStatus.VALIDATED });

    expect(mockedList).toHaveBeenCalledWith({ releaseDefinitionId: 'def-1', status: BundleStatus.VALIDATED }, '');
    expect(store.totalSize).toBe(1);
    expect(store.hasMore).toBe(false);
  });

  // The cursor is bound to its filter, so a filter change must restart from page 1.
  it('restarts from the first page when the filter changes', async () => {
    mockedList.mockResolvedValueOnce(page(['b1'], 'token-1', 3));
    const store = useBundlesStore();
    await store.load({ releaseDefinitionId: 'def-1' });
    expect(store.hasMore).toBe(true);

    mockedList.mockResolvedValueOnce(page(['b9'], '', 1));
    await store.load({ releaseDefinitionId: 'def-9' });

    expect(mockedList).toHaveBeenLastCalledWith({ releaseDefinitionId: 'def-9' }, '');
    expect(store.bundles.map((b) => b.id)).toEqual(['b9']);
    expect(store.hasMore).toBe(false);
  });

  it('appends the next page with the returned cursor', async () => {
    mockedList.mockResolvedValueOnce(page(['b1'], 'token-1', 2));
    mockedList.mockResolvedValueOnce(page(['b2'], '', 2));
    const store = useBundlesStore();
    await store.load({ releaseDefinitionId: 'def-1' });

    await store.appendNextPage();

    expect(mockedList).toHaveBeenLastCalledWith(expect.anything(), 'token-1');
    expect(store.bundles.map((b) => b.id)).toEqual(['b1', 'b2']);
    expect(store.hasMore).toBe(false);
  });

  // An unusable cursor means start over, not retry the same token.
  it('drops an unusable cursor and reloads the first page', async () => {
    mockedList.mockResolvedValueOnce(page(['b1'], 'stale', 2));
    const store = useBundlesStore();
    await store.load({ releaseDefinitionId: 'def-1' });

    mockedList.mockRejectedValueOnce(new ConnectError('invalid page token', Code.InvalidArgument));
    mockedList.mockResolvedValueOnce(page(['b1'], '', 2));
    await store.appendNextPage();

    expect(mockedList).toHaveBeenLastCalledWith(expect.anything(), '');
    expect(store.failure?.code).toBe('invalid_input');
    expect(store.hasMore).toBe(false);
  });

  // PERMISSION_DENIED must not look like "no bundles".
  it('distinguishes a permission refusal from an empty catalogue', async () => {
    mockedList.mockRejectedValue(new ConnectError('nope', Code.PermissionDenied));
    const store = useBundlesStore();

    await store.load({ releaseDefinitionId: 'def-1' });

    expect(store.failure?.code).toBe('permission_denied');
    expect(store.isEmpty).toBe(false);
    expect(store.failure?.message).toContain('不是「没有数据」');
  });

  it('does not request a detail while no definition has been given', async () => {
    const store = useBundlesStore();

    await store.openDetail(summary('b1'));

    expect(mockedDetail).not.toHaveBeenCalled();
    expect(store.detail).toBeNull();
  });

  it('opens the detail panel with the provenance fields, sending both ids', async () => {
    const store = useBundlesStore();
    await store.load({ releaseDefinitionId: 'def-1' });

    await store.openDetail(summary('b1'));

    // GetBundle needs the definition as well: it is the authorization key.
    expect(mockedDetail).toHaveBeenCalledWith('b1', 'def-1');
    expect(store.detail?.gitCommit).toBe('abc123');
    expect(store.detail?.sbomDigest).toBe('sb');
  });

  it('reports a detail failure without clearing the list', async () => {
    const store = useBundlesStore();
    await store.load({ releaseDefinitionId: 'def-1' });
    mockedDetail.mockRejectedValue(new ConnectError('missing', Code.NotFound));

    await store.openDetail(summary('b1'));

    expect(store.detailFailure?.code).toBe('not_found');
    expect(store.bundles).toHaveLength(1);
  });
});
