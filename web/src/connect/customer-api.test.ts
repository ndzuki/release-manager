import { create } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import type { Client } from '@connectrpc/connect';
import { describe, expect, it, vi } from 'vitest';
import { CustomerSchema } from '@/gen/common/v1/domain_pb';
import { OrchestratorService } from '@/gen/orchestrator/v1/orchestrator_pb';
import type { CustomerStatus } from '@/types/customer';
import {
  disableCustomer,
  listCustomerEvents,
  listCustomers,
  mapSaveError,
  setCustomerClientForTest,
  updateCustomer,
} from './customer-api';

function protoCustomer(overrides: Partial<{ name: string; slug: string; version: bigint; status: string }> = {}) {
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

/*
 * `Customer.status` is a free-form string on the wire and the server passes the stored
 * column through verbatim (internal/orchestrator/customer.go -> `string(c.Status)`).
 * The old mapping answered `active` for every value that was not `disabled`, and the
 * list page and the detail page both render that state and gate Edit/Disable on it —
 * so an unreadable status produced a confident, actionable "active" customer.
 */
describe('customer status mapping', () => {
  it('leaves a wire status it cannot read unknown instead of calling the customer active', async () => {
    const testClient = mockClient({
      listCustomers: vi.fn().mockResolvedValue({ customers: [protoCustomer({ status: 'suspended' })] }),
    });
    setCustomerClientForTest(testClient);

    const [customer] = await listCustomers();

    expect(customer.status).toBe('unknown');
    expect(customer.status).not.toBe('active');
    expect(customer.status).not.toBe('disabled');
  });

  // Positive control: the two real states must survive, or "unknown for everything"
  // would satisfy the case above.
  it('keeps reading the two statuses it knows', async () => {
    const testClient = mockClient({
      listCustomers: vi
        .fn()
        .mockResolvedValue({ customers: [protoCustomer({ status: 'active' }), protoCustomer({ status: 'disabled' })] }),
    });
    setCustomerClientForTest(testClient);

    const customers = await listCustomers();

    expect(customers.map((customer) => customer.status)).toEqual(['active', 'disabled']);
  });
});

/*
 * Type-level control (vitest erases types, so `vue-tsc -b` is the runner): the list and
 * detail pages both reduce the status to "disabled or not". That reduction is only safe
 * while `unknown` is a member of `CustomerStatus` — removing it turns this directive
 * into an unused one, which is a compile error.
 */
describe('the CustomerStatus union', () => {
  it('keeps unknown out of the two wire states', () => {
    // @ts-expect-error `unknown` is a third state; a two-state reduction must not swallow it.
    const wireState: Exclude<CustomerStatus, 'unknown'> = 'unknown' as CustomerStatus;

    expect(typeof wireState).toBe('string');
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
