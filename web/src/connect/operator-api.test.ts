import { create } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { Code, ConnectError } from '@connectrpc/connect';
import type { Client } from '@connectrpc/connect';
import { describe, expect, it, vi } from 'vitest';
import {
  OperatorErrorDetailSchema,
  OperatorLifecycleStatus,
  OperatorSessionStatus,
  OperatorSessionStatusReason,
  OperatorSummarySchema,
  OrchestratorService,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { listOperators, mapOperatorError, revokeOperator, setOperatorClientForTest } from './operator-api';

function mockClient(overrides: Partial<Client<typeof OrchestratorService>> = {}) {
  return {
    listOperators: vi.fn().mockResolvedValue({
      operators: [],
      nextPageToken: '',
      totalCount: 0,
      heartbeatIntervalSeconds: 15,
    }),
    ...overrides,
  } as unknown as Client<typeof OrchestratorService>;
}

describe('operator API', () => {
  it('maps service-owned status reasons without deriving from heartbeat timestamps', async () => {
    const lastHeartbeat = new Date('2026-07-20T01:02:03Z');
    const testClient = mockClient({
      listOperators: vi.fn().mockResolvedValue({
        operators: [create(OperatorSummarySchema, {
          id: 'operator-1',
          name: 'operator-one',
          customerId: 'customer-1',
          clusterId: 'cluster-1',
          clusterName: 'Staging',
          lifecycleStatus: OperatorLifecycleStatus.ACTIVE,
          sessionStatus: OperatorSessionStatus.OFFLINE,
          sessionStatusReason: OperatorSessionStatusReason.HEARTBEAT_TIMEOUT,
          lastHeartbeat: timestampFromDate(lastHeartbeat),
          registeredAt: timestampFromDate(new Date('2026-07-01T00:00:00Z')),
        })],
        nextPageToken: '',
        totalCount: 1,
        heartbeatIntervalSeconds: 15,
      }),
    });
    setOperatorClientForTest(testClient);

    const page = await listOperators('customer-1', 'cluster-1', { lifecycleStatus: null, sessionStatus: null });

    expect(page.operators[0]).toMatchObject({
      sessionStatus: 'offline',
      sessionStatusReason: 'heartbeat_timeout',
      lastHeartbeat: lastHeartbeat.toISOString(),
    });
  });

  it('encodes the no-session filter as optional UNSPECIFIED presence', async () => {
    const testClient = mockClient();
    setOperatorClientForTest(testClient);

    await listOperators('customer-1', 'cluster-1', { lifecycleStatus: null, sessionStatus: 'none' });

    expect(vi.mocked(testClient.listOperators).mock.calls[0]?.[0]).toMatchObject({
      customerId: 'customer-1',
      clusterId: 'cluster-1',
      sessionStatus: OperatorSessionStatus.UNSPECIFIED,
    });
  });

  it('decodes stable structured errors and keeps network failures retryable', () => {
    const structured = new ConnectError('a pending enrollment token already exists', Code.AlreadyExists, undefined, [
      {
        desc: OperatorErrorDetailSchema,
        value: { errorCode: 'pending_token_exists', fieldViolations: [] },
      },
    ]);
    expect(mapOperatorError(structured)).toMatchObject({ code: 'pending_token_exists', retryable: false });

    expect(mapOperatorError(ConnectError.from(new TypeError('Failed to fetch'), Code.Unavailable))).toMatchObject({
      code: 'network_error',
      retryable: true,
    });
  });

  it('maps unknown future lifecycle and session enum values to explicit unknown states', async () => {
    const testClient = mockClient({
      listOperators: vi.fn().mockResolvedValue({
        operators: [create(OperatorSummarySchema, {
          id: 'operator-future',
          name: 'operator-future',
          customerId: 'customer-1',
          clusterId: 'cluster-1',
          clusterName: 'Staging',
          lifecycleStatus: 99 as OperatorLifecycleStatus,
          sessionStatus: 99 as OperatorSessionStatus,
          sessionStatusReason: 99 as OperatorSessionStatusReason,
          registeredAt: timestampFromDate(new Date('2026-07-01T00:00:00Z')),
        })],
        nextPageToken: '',
        totalCount: 1,
        heartbeatIntervalSeconds: 15,
      }),
    });
    setOperatorClientForTest(testClient);

    const page = await listOperators('customer-1', 'cluster-1', { lifecycleStatus: null, sessionStatus: null });

    expect(page.operators[0]).toMatchObject({
      lifecycleStatus: 'unknown',
      sessionStatus: 'unknown',
      sessionStatusReason: 'unknown',
    });
  });

  it('returns the server changed flag with the authoritative revoked summary', async () => {
    const testClient = mockClient({
      revokeOperator: vi.fn().mockResolvedValue({
        operator: create(OperatorSummarySchema, {
          id: 'operator-1',
          name: 'operator-one',
          customerId: 'customer-1',
          clusterId: 'cluster-1',
          clusterName: 'Staging',
          lifecycleStatus: OperatorLifecycleStatus.REVOKED,
          sessionStatus: OperatorSessionStatus.REVOKED,
          sessionStatusReason: OperatorSessionStatusReason.CERTIFICATE_REVOKED,
          registeredAt: timestampFromDate(new Date('2026-07-01T00:00:00Z')),
        }),
        changed: false,
      }),
    });
    setOperatorClientForTest(testClient);

    const result = await revokeOperator('customer-1', 'cluster-1', 'operator-1', 'manual retry reason');

    expect(result).toMatchObject({
      changed: false,
      operator: { lifecycleStatus: 'revoked', sessionStatus: 'revoked', sessionStatusReason: 'certificate_revoked' },
    });
  });
});
