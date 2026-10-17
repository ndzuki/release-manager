import { createPinia, setActivePinia } from 'pinia';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { setOperationClientForTest } from '@/connect/operation-api';
import { Code, ConnectError, type Client } from '@connectrpc/connect';
import { create } from '@bufbuild/protobuf';
import {
  BundleService,
  BundleSummarySchema,
  CreateOperationResponseSchema,
  ListBundlesResponseSchema,
  OrchestratorService,
  type RollbackReleaseResponse,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { RollbackReleaseResponseSchema } from '@/gen/orchestrator/v1/orchestrator_pb';
import { BundleStatus } from '@/gen/common/v1/domain_pb';
import { useOperationFormStore } from './operationForm';
import * as valuesApi from '@/connect/values-revision';
import type { ValuesRevision } from '@/types/valuesRevision';

/*
 * TASK-280 / REQ-056 D10: setScope loads the approved ValuesRevisions, and validate()
 * now requires the pending id to be one of them. These dispatch tests drive the id
 * directly and predate the selector, so the loader must actually return it: the real
 * (unmocked) load fails in the test environment, which would rightly refuse the
 * INSTALL/UPGRADE submit exercised below.
 */
vi.mock('@/connect/values-revision', async (importOriginal) => {
  const original = await importOriginal<typeof valuesApi>();
  return { ...original, listApprovedValuesRevisions: vi.fn() };
});

const mockedApproved = vi.mocked(valuesApi.listApprovedValuesRevisions);

function approvedRevision(id: string): ValuesRevision {
  return {
    id,
    releaseDefinitionId: 'def-1',
    revision: 1,
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

type OperationsClient = Client<typeof OrchestratorService>;

function rollbackClients(
  rollbackImpl?: (request: unknown, options?: unknown) => Promise<RollbackReleaseResponse>,
): { operations: OperationsClient; bundles: Client<typeof BundleService>; createOperation: ReturnType<typeof vi.fn>; rollbackRelease: ReturnType<typeof vi.fn> } {
  const createOperation = vi.fn().mockResolvedValue(
    create(CreateOperationResponseSchema, { operationId: 'op-created', state: 'preflight', preflightId: 'pf-created' }),
  );
  const rollbackRelease = rollbackImpl
    ? vi.fn(rollbackImpl)
    : vi.fn().mockResolvedValue(
        create(RollbackReleaseResponseSchema, { operationId: 'op-rollback', fromRevision: 4, toRevision: 2, state: 'preflight' }),
      );
  const bundles = {
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
  } as unknown as Client<typeof BundleService>;
  return {
    operations: { createOperation, rollbackRelease } as unknown as OperationsClient,
    bundles,
    createOperation,
    rollbackRelease,
  };
}

describe('rollback form dispatch (TASK-153)', () => {
  let clients: ReturnType<typeof rollbackClients>;

  beforeEach(() => {
    sessionStorage.clear();
    setActivePinia(createPinia());
    clients = rollbackClients();
    setOperationClientForTest(clients.operations, clients.bundles);
    mockedApproved.mockReset().mockResolvedValue([approvedRevision('vr-1')]);
  });

  // AC-1/AC-2 + AC-5 negative control: ROLLBACK goes through its own RPC, never CreateOperation.
  it('dispatches ROLLBACK to RollbackRelease and never to CreateOperation', async () => {
    const store = useOperationFormStore();
    await store.setScope('def-1');
    store.setOperationType('ROLLBACK');
    store.fields.expectedCurrentRevision = 4;
    store.fields.targetRevision = 2;

    expect(store.openConfirmation()).toEqual({});

    const operationId = await store.submit();

    expect(operationId).toBe('op-rollback');
    expect(store.createdOperationId).toBe('op-rollback');
    expect(clients.rollbackRelease).toHaveBeenCalledTimes(1);
    expect(clients.createOperation).not.toHaveBeenCalled();

    const request = clients.rollbackRelease.mock.calls[0]?.[0] as Record<string, unknown>;
    expect(request.releaseDefinitionId).toBe('def-1');
    expect(request.targetRevision).toBe(2);
    expect(request.expectedCurrentRevision).toBe(4);
    expect(String(request.reason)).not.toBe('');
    // AC-2: the canonical request carries no values payload.
    expect(request.valuesRevisionId).toBe('');
    expect(request.valuesPatch).toBe('');
  });

  // AC-3: every invalid target is blocked before any RPC is attempted.
  it('blocks invalid target revisions before submitting', async () => {
    const store = useOperationFormStore();
    await store.setScope('def-1');
    store.setOperationType('ROLLBACK');
    store.fields.expectedCurrentRevision = 4;

    store.fields.targetRevision = null;
    expect(store.openConfirmation()).toEqual({ targetRevision: '请填写回滚目标 Revision' });
    expect(await store.submit()).toBeNull();

    store.fields.targetRevision = 0;
    expect(store.validate()).toEqual({ targetRevision: '目标 Revision 必须不小于 1' });

    store.fields.targetRevision = 1.5;
    expect(store.validate()).toEqual({ targetRevision: '目标 Revision 必须是整数' });

    store.fields.targetRevision = 4;
    expect(store.validate()).toEqual({ targetRevision: '目标 Revision 不能等于当前 Revision' });

    expect(clients.rollbackRelease).not.toHaveBeenCalled();
    expect(clients.createOperation).not.toHaveBeenCalled();
  });

  it('keeps CreateOperation for INSTALL and UPGRADE', async () => {
    const store = useOperationFormStore();
    await store.setScope('def-1');
    store.setOperationType('INSTALL');
    store.fields.bundleId = 'bundle-1';
    store.fields.valuesRevisionId = 'vr-1';

    expect(await store.submit()).toBe('op-created');
    expect(clients.createOperation).toHaveBeenCalledTimes(1);
    expect(clients.rollbackRelease).not.toHaveBeenCalled();
  });

  // AC-4: each rollback failure reason keeps its own readable message (never swallowed).
  it.each([
    ['target_revision_not_found', '目标 Revision 不存在，请刷新后重试'],
    ['release_busy', 'Release 有进行中的操作'],
    ['revision_conflict', 'Revision 已被更新，请刷新后重试'],
    ['permission_denied', '无权执行该操作'],
  ])('maps %s to a readable message', async (reason, expected) => {
    clients = rollbackClients(() => {
      const headers = new Headers({ 'X-Reason-Code': reason });
      return Promise.reject(new ConnectError('rollback failed', Code.FailedPrecondition, headers));
    });
    setOperationClientForTest(clients.operations, clients.bundles);

    const store = useOperationFormStore();
    await store.setScope('def-1');
    store.setOperationType('ROLLBACK');
    store.fields.expectedCurrentRevision = 4;
    store.fields.targetRevision = 2;

    expect(await store.submit()).toBeNull();
    expect(store.submitError?.code).toBe(reason);
    expect(store.submitError?.message).toBe(expected);
  });
});
