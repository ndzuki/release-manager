import { create } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import type { Client } from '@connectrpc/connect';
import { describe, expect, it, vi } from 'vitest';
import { CustomerSchema } from '@/gen/common/v1/domain_pb';
import { OrchestratorService } from '@/gen/orchestrator/v1/orchestrator_pb';
import {
  disableCustomer,
  listCustomerEvents,
  mapSaveError,
  setCustomerClientForTest,
  updateCustomer,
} from './customer-api';

function protoCustomer(overrides: Partial<{ name: string; slug: string; version: bigint }> = {}) {
  return create(CustomerSchema, {
    id: 'cust-1',
    name: 'Acme',
    slug: 'acme',
    status: 'active',
    version: 1n,
    ...overrides,
  });
}

function mockClient(overrides: Partial<Client<typeof OrchestratorService>> = {}) {
  return {
    createCustomer: vi.fn().mockResolvedValue({ customer: protoCustomer() }),
    getCustomer: vi.fn().mockResolvedValue({ customer: protoCustomer() }),
    listCustomers: vi.fn().mockResolvedValue({ customers: [protoCustomer()] }),
    updateCustomer: vi.fn().mockResolvedValue({ customer: protoCustomer() }),
    disableCustomer: vi.fn().mockResolvedValue({}),
    listCustomerEvents: vi.fn().mockResolvedValue({ events: [] }),
    ...overrides,
  } as unknown as Client<typeof OrchestratorService>;
}

describe('updateCustomer', () => {
  it('submits the typed customer contract with the observed version as bigint', async () => {
    const testClient = mockClient();
    setCustomerClientForTest(testClient);

    await updateCustomer('cust-1', { name: 'Renamed', slug: 'renamed', version: 2 });

    const payload = vi.mocked(testClient.updateCustomer).mock.calls[0]?.[0];
    expect(payload).toEqual({
      customerId: 'cust-1',
      name: 'Renamed',
      slug: 'renamed',
      expectedVersion: 2n,
    });
  });
});

describe('disableCustomer', () => {
  it('calls disable exactly once per invocation', async () => {
    const testClient = mockClient();
    setCustomerClientForTest(testClient);

    await disableCustomer('cust-1');

    expect(testClient.disableCustomer).toHaveBeenCalledTimes(1);
    expect(vi.mocked(testClient.disableCustomer).mock.calls[0]?.[0]).toEqual({ customerId: 'cust-1' });
  });
});

describe('listCustomerEvents', () => {
  it('returns the mapped event list', async () => {
    const testClient = mockClient();
    setCustomerClientForTest(testClient);

    const events = await listCustomerEvents('cust-1');

    expect(events).toEqual([]);
    expect(testClient.listCustomerEvents).toHaveBeenCalledWith({ customerId: 'cust-1' });
  });
});

describe('mapSaveError', () => {
  it('maps a network failure to a retryable draft-preserving error', () => {
    const mapped = mapSaveError(new ConnectError('boom', Code.Unavailable));
    expect(mapped.code).toBe('network_error');
  });

  it.each([
    ['optimistic_lock_conflict: data was modified by another user', 'optimistic_lock_conflict'],
    ['customer_disabled: customer is disabled', 'customer_disabled'],
    ['permission_denied: denied', 'permission_denied'],
    ['not_found: customer "x" not found', 'not_found'],
  ])('extracts %s from the server message', (message, code) => {
    expect(mapSaveError(new ConnectError(message)).code).toBe(code);
  });

  it('falls back to the Connect error code', () => {
    expect(mapSaveError(new ConnectError('denied', Code.PermissionDenied)).code).toBe('permission_denied');
  });
});
