import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { setOperationClientForTest } from '@/connect/operation-api';
import type { Client } from '@connectrpc/connect';
import {
  BundleService,
  BundleSummarySchema,
  CreateOperationResponseSchema,
  ListBundlesResponseSchema,
  OrchestratorService,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { BundleStatus } from '@/gen/common/v1/domain_pb';
import { create } from '@bufbuild/protobuf';
import { useOperationFormStore } from './operationForm';
import * as valuesApi from '@/connect/values-revision';
import type { ValuesRevision } from '@/types/valuesRevision';

/*
 * The approved-revision list (REQ-056 D10) is loaded through this wrapper; the
 * store must not fall back to a free-text id, and a failed list load must not hide
 * the bundles the form also needs.
 */
vi.mock('@/connect/values-revision', async (importOriginal) => {
  const original = await importOriginal<typeof valuesApi>();
  return { ...original, listApprovedValuesRevisions: vi.fn() };
});

const mockedApproved = vi.mocked(valuesApi.listApprovedValuesRevisions);

function approvedRevision(id: string, revision = 1): ValuesRevision {
  return {
    id,
    releaseDefinitionId: 'def-1',
    revision,
    stateVersion: '3',
    document: '{}',
    valuesDigest: `sha256:${id}`,
    status: 'approved',
    parentRevisionId: null,
    secretRefs: [],
    createdByUserId: 'u-1',
    createdAt: '2026-10-01T00:00:00Z',
    convergenceTaskIds: [],
    lockedPaths: [],
  };
}

function mockClients(): { operations: Client<typeof OrchestratorService>; bundles: Client<typeof BundleService> } {
  return {
    operations: {
      createOperation: vi.fn().mockResolvedValue(create(CreateOperationResponseSchema, {
        operationId: 'op-created', state: 'preflight', preflightId: 'pf-created',
      })),
    } as unknown as Client<typeof OrchestratorService>,
    bundles: {
      listBundles: vi.fn().mockResolvedValue(create(ListBundlesResponseSchema, {
        bundles: [create(BundleSummarySchema, {
          id: 'bundle-1',
          name: 'app',
          digest: { algorithm: 'sha256', value: 'sha256:bundle' },
          status: BundleStatus.VALIDATED,
          chartRef: 'oci://registry/app',
          chartVersion: '1.0.0',
          chartDigest: 'sha256:chart',
          images: [{ ref: 'registry/app:v1', digest: 'sha256:image', valuesPath: 'image' }],
        })],
      })),
    } as unknown as Client<typeof BundleService>,
  };
}

describe('operation form store', () => {
  beforeEach(() => {
    sessionStorage.clear();
    setActivePinia(createPinia());
    const clients = mockClients();
    setOperationClientForTest(clients.operations, clients.bundles);
    mockedApproved.mockReset().mockResolvedValue([approvedRevision('vr-1'), approvedRevision('vr-2', 2)]);
  });

  it('enforces operation-specific required fields', async () => {
    const store = useOperationFormStore();
    await store.setScope('def-1');

    expect(store.validate()).toEqual({ bundleId: '请选择制品', valuesRevisionId: '请选择已审批的配置版本' });

    store.setOperationType('UPGRADE');
    store.fields.bundleId = 'bundle-1';
    store.fields.valuesRevisionId = 'vr-1';
    expect(store.validate()).toEqual({ expectedCurrentRevision: '无法确定当前 Revision' });

    store.setOperationType('ROLLBACK');
    store.fields.expectedCurrentRevision = 4;
    // Canonical rollback (AC-056-08): the values revision is not collected, and a target
    // revision that differs from the current one is required instead.
    expect(store.fields.valuesRevisionId).toBeNull();
    expect(store.fields.targetRevision).toBeNull();
    expect(store.validate()).toEqual({ targetRevision: '请填写回滚目标 Revision' });

    store.fields.targetRevision = 4;
    expect(store.validate()).toEqual({ targetRevision: '目标 Revision 不能等于当前 Revision' });

    store.fields.targetRevision = 3;
    expect(store.fields.bundleId).toBe('bundle-1');
    expect(store.fields.patch).toEqual([]);
    expect(store.validate()).toEqual({});
  });

  it('rejects non-bundle image patches before submitting', async () => {
    const store = useOperationFormStore();
    await store.setScope('def-1');
    store.fields.bundleId = 'bundle-1';
    store.fields.valuesRevisionId = 'vr-1';
    store.fields.patch = [{ path: 'sidecar.image.tag', value: 'v2', kind: 'LITERAL' }];

    expect(store.openConfirmation()).toEqual({ patch: 'Patch 引用了 Bundle 外镜像', patchIndex: 0 });
    expect(store.step).toBe('form');
  });

  it('restores only safe draft fields and never persists an idempotency key', async () => {
    sessionStorage.setItem('op-draft:def-1', JSON.stringify({
      operationType: 'UPGRADE',
      bundleId: 'bundle-1',
      valuesRevisionId: 'vr-1',
      patch: [
        { path: 'image.tag', value: 'v2', kind: 'LITERAL' },
        { path: 'app.password', value: 'plain', kind: 'LITERAL' },
      ],
      idempotencyKey: 'must-not-restore',
    }));

    const store = useOperationFormStore();
    await store.setScope('def-1');

    expect(store.fields.operationType).toBe('UPGRADE');
    expect(store.fields.patch).toEqual([{ path: 'image.tag', value: 'v2', kind: 'LITERAL' }]);
    expect(sessionStorage.getItem('op-draft:def-1')).not.toContain('idempotencyKey');
  });

  it('clears the previous release draft when the route scope changes', async () => {
    const store = useOperationFormStore();
    await store.setScope('def-1');
    store.fields.bundleId = 'bundle-1';
    store.fields.valuesRevisionId = 'vr-1';
    await vi.waitFor(() => expect(sessionStorage.getItem('op-draft:def-1')).not.toBeNull());

    await store.setScope('def-2');

    expect(sessionStorage.getItem('op-draft:def-1')).toBeNull();
    expect(store.releaseDefinitionId).toBe('def-2');
    expect(store.fields.bundleId).toBeNull();
    expect(store.fields.valuesRevisionId).toBeNull();
  });

  it('does not submit when confirmation is cancelled', async () => {
    const clients = mockClients();
    setOperationClientForTest(clients.operations, clients.bundles);
    const store = useOperationFormStore();
    await store.setScope('def-1');
    store.fields.bundleId = 'bundle-1';
    store.fields.valuesRevisionId = 'vr-1';

    expect(store.openConfirmation()).toEqual({});
    store.cancelConfirmation();

    expect(store.step).toBe('form');
    expect(clients.operations.createOperation).not.toHaveBeenCalled();
  });

  // REQ-056 D10: the form's revision ids come from the approved-revision loader,
  // never from a typed value.
  it('loads the approved revisions for the release scope', async () => {
    const store = useOperationFormStore();

    await store.setScope('def-1');

    expect(mockedApproved).toHaveBeenCalledWith('def-1');
    expect(store.approvedRevisions.map((revision) => revision.id)).toEqual(['vr-1', 'vr-2']);
    expect(store.revisionsError).toBeNull();
  });

  // A failing revision list must not hide the bundles: the selector renders its own
  // empty/error state instead.
  it('keeps the bundles when the approved-revision load fails', async () => {
    mockedApproved.mockRejectedValue(new Error('revision list unavailable'));
    const store = useOperationFormStore();

    await store.setScope('def-1');

    expect(store.availableBundles.map((bundle) => bundle.bundleId)).toEqual(['bundle-1']);
    expect(store.approvedRevisions).toEqual([]);
    expect(store.revisionsError).toContain('revision list unavailable');
  });

  /*
   * REQ-056 D10 invariant. Picking from the selector is not enough: a draft written
   * by the old free-text form, or a revision superseded after it was selected, must
   * never reach CreateOperation only to be refused with revision_not_approved. The
   * three entries are covered here -- the restored draft (below), the empty approved
   * list, and an id written straight into the field.
   */
  it('drops a restored draft revision that is not in the approved list and refuses to submit', async () => {
    mockedApproved.mockResolvedValue([approvedRevision('vr-1')]);
    const clients = mockClients();
    setOperationClientForTest(clients.operations, clients.bundles);
    sessionStorage.setItem('op-draft:def-1', JSON.stringify({
      operationType: 'UPGRADE',
      bundleId: 'bundle-1',
      valuesRevisionId: 'vr-typed-before-upgrade',
      patch: [],
      targetRevision: null,
    }));

    const store = useOperationFormStore();
    await store.setScope('def-1');
    store.fields.expectedCurrentRevision = 3;

    expect(store.fields.valuesRevisionId).toBeNull();
    expect(store.validate()).toEqual({ valuesRevisionId: '请选择已审批的配置版本' });
    expect(await store.submit()).toBeNull();
    expect(clients.operations.createOperation).not.toHaveBeenCalled();
  });

  // The empty list is what disables the selector; a value only a stale draft can
  // still carry must not make the form submittable behind that disabled control.
  it('refuses to submit a draft revision when no revision is approved', async () => {
    mockedApproved.mockResolvedValue([]);
    const clients = mockClients();
    setOperationClientForTest(clients.operations, clients.bundles);
    sessionStorage.setItem('op-draft:def-1', JSON.stringify({
      operationType: 'UPGRADE',
      bundleId: 'bundle-1',
      valuesRevisionId: 'vr-1',
      patch: [],
      targetRevision: null,
    }));

    const store = useOperationFormStore();
    await store.setScope('def-1');
    store.fields.expectedCurrentRevision = 3;

    expect(store.approvedRevisions).toEqual([]);
    expect(store.fields.valuesRevisionId).toBeNull();
    expect(store.validate()).toEqual({ valuesRevisionId: '请选择已审批的配置版本' });
    expect(await store.submit()).toBeNull();
    expect(clients.operations.createOperation).not.toHaveBeenCalled();
  });

  // Negative control for the two above: an approved draft id still submits, so the
  // guard is not a blanket refusal of restored drafts.
  it('submits a restored draft revision that is in the approved list', async () => {
    mockedApproved.mockResolvedValue([approvedRevision('vr-1')]);
    const clients = mockClients();
    setOperationClientForTest(clients.operations, clients.bundles);
    sessionStorage.setItem('op-draft:def-1', JSON.stringify({
      operationType: 'UPGRADE',
      bundleId: 'bundle-1',
      valuesRevisionId: 'vr-1',
      patch: [],
      targetRevision: null,
    }));

    const store = useOperationFormStore();
    await store.setScope('def-1');
    store.fields.expectedCurrentRevision = 3;

    expect(store.fields.valuesRevisionId).toBe('vr-1');
    expect(store.validate()).toEqual({});
    await expect(store.submit()).resolves.toBe('op-created');
    expect(clients.operations.createOperation).toHaveBeenCalledTimes(1);
    const request = vi.mocked(clients.operations.createOperation).mock.calls[0]?.[0];
    expect(request?.valuesRevisionId).toBe('vr-1');
  });

  /*
   * The async-ordering risk of the reconciliation: `approvedRevisions` is empty while
   * the list is in flight, so clearing the draft at that moment would silently drop
   * every legitimate restored selection. The value must survive the await; only a
   * resolved list may clear it.
   */
  it('keeps a restored draft revision while the approved list is still loading', async () => {
    let resolveApproved: (revisions: ValuesRevision[]) => void = () => {};
    mockedApproved.mockImplementation(
      () => new Promise<ValuesRevision[]>((resolve) => { resolveApproved = resolve; }),
    );
    sessionStorage.setItem('op-draft:def-1', JSON.stringify({
      operationType: 'UPGRADE',
      bundleId: 'bundle-1',
      valuesRevisionId: 'vr-1',
      patch: [],
      targetRevision: null,
    }));

    const store = useOperationFormStore();
    const scope = store.setScope('def-1');
    await vi.waitFor(() => expect(mockedApproved).toHaveBeenCalled());

    expect(store.revisionsLoading).toBe(true);
    expect(store.fields.valuesRevisionId).toBe('vr-1');
    store.fields.expectedCurrentRevision = 3;
    // While the list is unknown the submit is fail-closed (validate() cannot confirm
    // membership), but the draft value itself is untouched.
    expect(store.validate()).toEqual({ valuesRevisionId: '请选择已审批的配置版本' });

    resolveApproved([approvedRevision('vr-1')]);
    await scope;

    expect(store.fields.valuesRevisionId).toBe('vr-1');
    expect(store.validate()).toEqual({});
  });

  // validate() is the always-on guard for ids that never came from the draft: the
  // store's own field is writable, so membership cannot live in the loader alone.
  it('refuses a revision id written straight into the field', async () => {
    const clients = mockClients();
    setOperationClientForTest(clients.operations, clients.bundles);
    const store = useOperationFormStore();
    await store.setScope('def-1');
    store.fields.bundleId = 'bundle-1';
    store.fields.valuesRevisionId = 'vr-not-approved';

    expect(store.validate()).toEqual({ valuesRevisionId: '请选择已审批的配置版本' });
    expect(await store.submit()).toBeNull();
    expect(clients.operations.createOperation).not.toHaveBeenCalled();
  });
});
