import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createPinia, setActivePinia } from 'pinia';
import { ConnectError, Code } from '@connectrpc/connect';
import type * as CustomerApi from '@/connect/customer-api';
import type { CustomerEvent, CustomerSummary } from '@/types/customer';
import {
  createCustomer,
  disableCustomer,
  getCustomer,
  listCustomerEvents,
  listCustomers,
  updateCustomer,
} from '@/connect/customer-api';
import { useCustomerStore } from './customers';

vi.mock('@/connect/customer-api', async (importOriginal) => {
  const original = await importOriginal<typeof CustomerApi>();
  return {
    ...original,
    createCustomer: vi.fn(),
    disableCustomer: vi.fn(),
    getCustomer: vi.fn(),
    listCustomerEvents: vi.fn(),
    listCustomers: vi.fn(),
    updateCustomer: vi.fn(),
  };
});

function customer(overrides: Partial<CustomerSummary> = {}): CustomerSummary {
  return {
    id: 'cust-1',
    name: 'Acme',
    slug: 'acme',
    status: 'active',
    version: 1,
    createdAt: '2026-08-01T00:00:00.000Z',
    ...overrides,
  };
}

function event(overrides: Partial<CustomerEvent> = {}): CustomerEvent {
  return {
    id: 'ev-1',
    customerId: 'cust-1',
    eventType: 'customer_created',
    createdAt: '2026-08-01T00:00:00.000Z',
    ...overrides,
  };
}

beforeEach(() => {
  setActivePinia(createPinia());
  vi.clearAllMocks();
  vi.mocked(createCustomer).mockResolvedValue(customer());
  vi.mocked(disableCustomer).mockResolvedValue(undefined);
  vi.mocked(getCustomer).mockResolvedValue(customer());
  vi.mocked(listCustomerEvents).mockResolvedValue([event()]);
  vi.mocked(listCustomers).mockResolvedValue([]);
});

describe('customer store list', () => {
  it('loads customers into the list state', async () => {
    vi.mocked(listCustomers).mockResolvedValue([customer({ id: 'a' }), customer({ id: 'b' })]);
    const store = useCustomerStore();

    await store.loadList();

    expect(store.customers.map((item) => item.id)).toEqual(['a', 'b']);
    expect(store.loading).toBe(false);
    expect(store.error).toBeNull();
  });

  it('maps a permission failure to the forbidden state', async () => {
    vi.mocked(listCustomers).mockRejectedValue(new ConnectError('permission_denied: denied', Code.PermissionDenied));
    const store = useCustomerStore();

    await store.loadList();

    expect(store.forbidden).toBe(true);
    expect(store.error).toBeTruthy();
  });

  it('surfaces a network failure as a retryable error', async () => {
    vi.mocked(listCustomers).mockRejectedValue(new ConnectError('boom', Code.Unavailable));
    const store = useCustomerStore();

    await store.loadList();

    expect(store.forbidden).toBe(false);
    expect(store.error).toBe('Unable to connect to the server. Your draft has been preserved.');
  });
});

describe('customer store detail', () => {
  it('loads the customer and seeds the draft with the server version', async () => {
    vi.mocked(getCustomer).mockResolvedValue(customer({ name: 'Acme', version: 3 }));
    const store = useCustomerStore();

    await store.loadCustomer('cust-1');

    expect(store.current?.name).toBe('Acme');
    expect(store.draft).toEqual({ name: 'Acme', slug: 'acme', version: 3 });
    expect(store.serverVersion).toBe(3);
  });

  it('loads history independently of the detail request', async () => {
    vi.mocked(listCustomerEvents).mockResolvedValue([event({ eventType: 'customer_disabled' })]);
    const store = useCustomerStore();

    await store.loadHistory('cust-1');

    expect(store.history.map((item) => item.eventType)).toEqual(['customer_disabled']);
    expect(store.historyLoading).toBe(false);
  });
});

describe('customer store save', () => {
  it('creates a customer and refreshes the list', async () => {
    vi.mocked(createCustomer).mockResolvedValue(customer({ id: 'new-1', name: 'New Co' }));
    const store = useCustomerStore();
    store.startCreate();
    store.draft = { name: 'New Co', slug: 'new-co', version: 0 };

    const saved = await store.save();

    expect(saved?.id).toBe('new-1');
    expect(vi.mocked(createCustomer)).toHaveBeenCalledWith({ name: 'New Co', slug: 'new-co', version: 0 });
    expect(store.customers[0]?.id).toBe('new-1');
    expect(store.saveError).toBeNull();
  });

  it('preserves the draft and reports the conflict on a stale version', async () => {
    vi.mocked(updateCustomer).mockRejectedValue(
      new ConnectError('optimistic_lock_conflict: data was modified by another user', Code.Aborted),
    );
    const store = useCustomerStore();
    store.current = customer({ version: 2 });
    store.draft = { name: 'Edited', slug: 'acme', version: 2 };

    const saved = await store.save('cust-1');

    expect(saved).toBeNull();
    expect(store.saveError?.code).toBe('optimistic_lock_conflict');
    expect(store.draft).toEqual({ name: 'Edited', slug: 'acme', version: 2 });
    expect(store.saving).toBe(false);
  });

  it('rebases the draft version after a refresh', async () => {
    vi.mocked(getCustomer).mockResolvedValue(customer({ version: 5 }));
    const store = useCustomerStore();
    store.draft = { name: 'Edited', slug: 'acme', version: 2 };

    await store.refreshCustomer('cust-1');

    expect(store.draft).toEqual({ name: 'Edited', slug: 'acme', version: 5 });
    expect(store.saveError).toBeNull();
  });
});

describe('customer store disable', () => {
  it('marks the customer disabled and reloads history after a single call', async () => {
    const store = useCustomerStore();
    store.current = customer();
    store.draft = { name: 'Acme', slug: 'acme', version: 1 };

    await store.disable('cust-1');

    expect(vi.mocked(disableCustomer)).toHaveBeenCalledTimes(1);
    expect(store.current?.status).toBe('disabled');
    expect(vi.mocked(listCustomerEvents)).toHaveBeenCalledWith('cust-1');
    expect(store.disabling).toBe(false);
  });

  it('ignores a second disable call while one is pending', async () => {
    let resolveDisable: () => void = () => {};
    vi.mocked(disableCustomer).mockImplementation(
      () => new Promise<void>((resolve) => { resolveDisable = resolve; }),
    );
    const store = useCustomerStore();

    const first = store.disable('cust-1');
    const second = store.disable('cust-1');
    resolveDisable();
    await Promise.all([first, second]);

    expect(vi.mocked(disableCustomer)).toHaveBeenCalledTimes(1);
  });

  it('surfaces the failure without flipping local state', async () => {
    vi.mocked(disableCustomer).mockRejectedValue(new ConnectError('permission_denied: denied', Code.PermissionDenied));
    const store = useCustomerStore();
    store.current = customer();

    await store.disable('cust-1');

    expect(store.current?.status).toBe('active');
    expect(store.disableError).toBeTruthy();
    expect(store.disabling).toBe(false);
  });
});
