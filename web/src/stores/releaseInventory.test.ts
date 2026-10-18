import { create, fromBinary } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { orchestratorClient } from '@/connect/client';
import {
  ListReleasesResponseSchema,
  ReleaseInventoryStatus,
  ReleaseSummarySchema,
  type ReleaseSummary as ProtoReleaseSummary,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { useReleaseInventoryStore } from './releaseInventory';

vi.mock('@/connect/client', () => ({
  orchestratorClient: { listReleases: vi.fn() },
}));

const listReleases = vi.mocked(orchestratorClient.listReleases);

function summary(status: ReleaseInventoryStatus): ProtoReleaseSummary {
  return create(ReleaseSummarySchema, {
    namespace: 'apps',
    name: 'api',
    chart: 'api',
    chartVersion: '1.0.0',
    revision: 1,
    status,
  });
}

/**
 * A release as the wire delivers it when the server never set `status`: proto3 has no
 * presence for a scalar, so the decoder yields the zero value (UNSPECIFIED).
 */
function summaryWithoutStatus(): ProtoReleaseSummary {
  return fromBinary(ReleaseSummarySchema, new Uint8Array());
}

async function loadedStatus(release: ProtoReleaseSummary) {
  listReleases.mockResolvedValue(
    create(ListReleasesResponseSchema, { releases: [release], totalCount: 1 }),
  );
  const store = useReleaseInventoryStore();
  store.setScope('customer-1', 'cluster-1');
  await store.load();
  return store.releases[0]!.status;
}

/*
 * UX-061: the inventory turns the wire status into the badge's status. The mapping
 * must not read "the server said something this console does not know" as Active:
 * an omitted field (proto3 sends the zero value for it), an explicit UNSPECIFIED,
 * and an enum member a newer server added all have to land on the neutral status
 * instead of the green "生效中".
 */
describe('release inventory status mapping (UX-061)', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    listReleases.mockReset();
  });

  it.each([
    [ReleaseInventoryStatus.ACTIVE, 'active'],
    [ReleaseInventoryStatus.MISSING, 'missing'],
    [ReleaseInventoryStatus.OUT_OF_SYNC, 'out_of_sync'],
  ])('maps the known wire status %s to %s', async (status, expected) => {
    await expect(loadedStatus(summary(status))).resolves.toBe(expected);
  });

  it('maps an omitted status field to unknown, never active', async () => {
    const release = summaryWithoutStatus();
    expect(release.status).toBe(ReleaseInventoryStatus.UNSPECIFIED);

    await expect(loadedStatus(release)).resolves.toBe('unknown');
  });

  it('maps an explicit UNSPECIFIED to unknown, never active', async () => {
    await expect(loadedStatus(summary(ReleaseInventoryStatus.UNSPECIFIED))).resolves.toBe('unknown');
  });

  // proto3 enums are open in protobuf-es: a value this console has never heard of
  // survives decoding as its number instead of failing the decode. 0x30 is field 6
  // (status) with wire type 0, and 0x04 is the unknown value.
  it('maps an unrecognised wire value to unknown, never active', async () => {
    const release = fromBinary(ReleaseSummarySchema, new Uint8Array([0x30, 0x04]));
    expect(release.status).toBe(4);

    await expect(loadedStatus(release)).resolves.toBe('unknown');
  });

  // "unknown" describes what the console could not read, so it can never be a
  // query: UNSPECIFIED means "no filter" to the server, and sending it there would
  // return every row as if the filter had matched.
  it('keeps unknown display-only, never a status filter', async () => {
    listReleases.mockResolvedValue(create(ListReleasesResponseSchema, {}));
    const store = useReleaseInventoryStore();
    store.setScope('customer-1', 'cluster-1');

    await store.load();
    expect(listReleases).toHaveBeenLastCalledWith(
      expect.objectContaining({ statusFilter: ReleaseInventoryStatus.UNSPECIFIED }),
    );

    store.setStatusFilter('active');
    await store.load();
    expect(listReleases).toHaveBeenLastCalledWith(
      expect.objectContaining({ statusFilter: ReleaseInventoryStatus.ACTIVE }),
    );
  });
});

/*
 * UX-057 (W16): refresh was reachable from four controls and every click issued
 * its own ListReleases; a double click could resolve out of order. One in-flight
 * refresh must serve every caller.
 */
describe('release inventory refresh de-duplication (UX-057)', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    listReleases.mockReset();
  });

  it('shares one request between concurrent refreshes', async () => {
    let resolveRequest: ((value: unknown) => void) | undefined;
    listReleases.mockReturnValue(
      new Promise((resolve) => {
        resolveRequest = resolve as (value: unknown) => void;
      }) as never,
    );
    const store = useReleaseInventoryStore();
    store.setScope('customer-1', 'cluster-1');

    const first = store.refresh();
    const second = store.refresh();
    expect(listReleases).toHaveBeenCalledTimes(1);

    resolveRequest?.(create(ListReleasesResponseSchema, { totalCount: 1 }));
    await Promise.all([first, second]);

    // The shared request is complete, so the NEXT refresh is a new request.
    listReleases.mockResolvedValue(create(ListReleasesResponseSchema, { totalCount: 2 }));
    await store.refresh();
    expect(listReleases).toHaveBeenCalledTimes(2);
  });
});

/*
 * UX-015 / H4: a failed read must leave the user a correlation id. The copy
 * alone ("网络错误…") is not reportable; ErrorState renders `details`, so the
 * store has to carry the correlation line.
 */
describe('release inventory failure correlation (UX-015)', () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    listReleases.mockReset();
  });

  it('keeps the request id of a failed read in errorDetails', async () => {
    listReleases.mockRejectedValue(
      new ConnectError('boom', Code.Unavailable, { 'X-Request-ID': 'req-42' }),
    );
    const store = useReleaseInventoryStore();
    store.setScope('customer-1', 'cluster-1');

    await store.load();

    expect(store.error).toBe('网络错误，请检查连接后重试');
    expect(store.errorDetails).toContain('requestId=req-42');
    expect(store.errorDetails).toContain('procedure=ListReleases');
  });

  it('clears the correlation line on the next successful read', async () => {
    listReleases.mockRejectedValueOnce(new ConnectError('boom', Code.Unavailable, { 'X-Request-ID': 'req-42' }));
    listReleases.mockResolvedValue(create(ListReleasesResponseSchema, { totalCount: 1 }));
    const store = useReleaseInventoryStore();
    store.setScope('customer-1', 'cluster-1');

    await store.load();
    expect(store.errorDetails).toContain('requestId=req-42');

    await store.refresh();
    expect(store.errorDetails).toBeNull();
  });
});
