import { create, fromBinary } from '@bufbuild/protobuf';
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
