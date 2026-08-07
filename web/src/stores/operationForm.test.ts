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
  });

  it('enforces operation-specific required fields', async () => {
    const store = useOperationFormStore();
    await store.setScope('def-1');

    expect(store.validate()).toEqual({ bundleId: '请选择制品', valuesRevisionId: '请填写已审批的配置版本 ID' });

    store.setOperationType('UPGRADE');
    store.fields.bundleId = 'bundle-1';
    store.fields.valuesRevisionId = 'vr-1';
    expect(store.validate()).toEqual({ expectedCurrentRevision: '无法确定当前 Revision' });

    store.setOperationType('ROLLBACK');
    store.fields.expectedCurrentRevision = 4;
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
});
