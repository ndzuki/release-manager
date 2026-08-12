import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createPinia, setActivePinia } from 'pinia';
import type * as OperatorApi from '@/connect/operator-api';
import {
  createEnrollmentToken,
  getEnrollmentTokenStatus,
  getOperator,
  listOperators,
  revokeOperator,
  revokePendingEnrollmentToken,
} from '@/connect/operator-api';
import type { EnrollmentTokenResult, OperatorDetail, OperatorPage } from '@/types/operator';
import { useOperatorStore } from './operator';

vi.mock('@/connect/operator-api', async (importOriginal) => {
  const original = await importOriginal<typeof OperatorApi>();
  return {
    ...original,
    createEnrollmentToken: vi.fn(),
    getEnrollmentTokenStatus: vi.fn(),
    getOperator: vi.fn(),
    listOperators: vi.fn(),
    revokeOperator: vi.fn(),
    revokePendingEnrollmentToken: vi.fn(),
  };
});

const detail: OperatorDetail = {
  id: 'operator-1',
  name: 'operator-one',
  customerId: 'customer-1',
  clusterId: 'cluster-1',
  clusterName: 'Staging',
  lifecycleStatus: 'active',
  sessionStatus: 'offline',
  sessionStatusReason: 'heartbeat_timeout',
  lastHeartbeat: '2026-07-20T01:02:03.000Z',
  registeredAt: '2026-07-01T00:00:00.000Z',
  supersededAt: null,
  revokedAt: null,
  supersededBy: null,
  revokeReason: null,
  instanceId: 'instance-1',
  version: '1.0.0',
  capabilities: { helm: 'true' },
};

const tokenResult: EnrollmentTokenResult = {
  token: 'plaintext-token',
  expiresAt: '2026-07-27T13:00:00.000Z',
  customerId: 'customer-1',
  clusterId: 'cluster-1',
  clusterName: 'Staging',
  operatorEndpoint: 'https://operator.example.com',
  installCommandTemplateVersion: 'v1',
  installCommandTemplate: 'release-operator --enrollment-token ${ENROLLMENT_TOKEN}',
};

beforeEach(() => {
  setActivePinia(createPinia());
  vi.clearAllMocks();
  vi.mocked(listOperators).mockResolvedValue({
    operators: [detail],
    nextPageToken: null,
    totalCount: 1,
    heartbeatIntervalSeconds: 15,
  } satisfies OperatorPage);
  vi.mocked(getOperator).mockResolvedValue({ operator: detail, heartbeatIntervalSeconds: 15 });
  vi.mocked(getEnrollmentTokenStatus).mockResolvedValue({
    state: 'none',
    createdAt: null,
    expiresAt: null,
    createdByDisplayName: null,
  });
  vi.mocked(createEnrollmentToken).mockResolvedValue(tokenResult);
  vi.mocked(revokePendingEnrollmentToken).mockResolvedValue(true);
  vi.mocked(revokeOperator).mockResolvedValue({ operator: { ...detail, lifecycleStatus: 'revoked', sessionStatus: 'revoked' }, changed: true });
});

describe('operator store', () => {
  it('never stores plaintext token in Pinia state', async () => {
    const store = useOperatorStore();
    store.enrollmentForm = { operatorName: 'operator-one', ttlMinutes: 60 };

    const result = await store.generateToken('customer-1', 'cluster-1');

    expect(result?.token).toBe('plaintext-token');
    expect(JSON.stringify(store.$state)).not.toContain('plaintext-token');
    expect(store.pending?.state).toBe('pending');
  });

  it('preserves visible list data and stops automatic polling after a failed refresh', async () => {
    const store = useOperatorStore();
    expect(await store.loadList('customer-1', 'cluster-1')).toBe(true);
    vi.mocked(listOperators).mockRejectedValueOnce(new TypeError('Failed to fetch'));

    expect(await store.loadList('customer-1', 'cluster-1')).toBe(false);

    expect(store.operators).toEqual([detail]);
    expect(store.error?.code).toBe('network_error');
  });

  it('keeps a revoke draft retryable when the request fails', async () => {
    const store = useOperatorStore();
    vi.mocked(revokeOperator).mockRejectedValueOnce(new TypeError('Failed to fetch'));

    expect(await store.revokeOperator('customer-1', 'cluster-1', 'operator-1', 'security incident')).toBe(false);
    expect(store.error?.code).toBe('network_error');
    expect(store.current).toBeNull();
  });

  it('preserves loaded detail metadata when revocation returns only a summary', async () => {
    const store = useOperatorStore();
    expect(await store.loadDetail('customer-1', 'cluster-1', 'operator-1')).toBe(true);

    expect(await store.revokeOperator('customer-1', 'cluster-1', 'operator-1', 'security incident')).toBe(true);

    expect(store.current).toMatchObject({
      lifecycleStatus: 'revoked',
      sessionStatus: 'revoked',
      instanceId: 'instance-1',
      version: '1.0.0',
      capabilities: { helm: 'true' },
      revokeReason: 'security incident',
    });
  });

  it('does not overwrite the first revoke reason after an idempotent retry', async () => {
    const store = useOperatorStore();
    expect(await store.loadDetail('customer-1', 'cluster-1', 'operator-1')).toBe(true);
    store.current = { ...detail, lifecycleStatus: 'revoked', sessionStatus: 'revoked', revokeReason: 'first reason' };
    vi.mocked(revokeOperator).mockResolvedValueOnce({
      operator: { ...detail, lifecycleStatus: 'revoked', sessionStatus: 'revoked' },
      changed: false,
    });

    expect(await store.revokeOperator('customer-1', 'cluster-1', 'operator-1', 'manual retry reason')).toBe(true);

    expect(store.current?.revokeReason).toBe('first reason');
  });
});
