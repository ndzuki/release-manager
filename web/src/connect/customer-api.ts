import { t } from '@/i18n/messages';
import { Code, ConnectError, createClient } from '@connectrpc/connect';
import type { Client } from '@connectrpc/connect';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import type { Customer as ProtoCustomer, CustomerEvent as ProtoCustomerEvent } from '@/gen/common/v1/domain_pb';
import { OrchestratorService } from '@/gen/orchestrator/v1/orchestrator_pb';
import { transport } from '@/connect/client';
import type { CustomerEvent, CustomerFormInput, CustomerStatus, CustomerSummary, SaveError } from '@/types/customer';

let client: Client<typeof OrchestratorService> = createClient(OrchestratorService, transport);

export function setCustomerClientForTest(replacement: Client<typeof OrchestratorService>) {
  client = replacement;
}

/**
 * Only a wire status this console can read becomes a state. `Customer.status` is a
 * free-form string (api/proto/common/v1/domain.proto) that the server passes through
 * verbatim (internal/orchestrator/customer.go -> `string(c.Status)`), so "not
 * disabled" is not evidence of active: an unreadable value stays `unknown` rather
 * than being reported as an account state nobody established.
 */
function customerStatus(status: string): CustomerStatus {
  return status === 'active' || status === 'disabled' ? status : 'unknown';
}

function mapCustomer(customer: ProtoCustomer): CustomerSummary {
  return {
    id: customer.id,
    name: customer.name,
    slug: customer.slug,
    status: customerStatus(customer.status),
    version: Number(customer.version),
    createdAt: customer.createdAt ? timestampDate(customer.createdAt).toISOString() : undefined,
  };
}

function mapEvent(event: ProtoCustomerEvent): CustomerEvent {
  return {
    id: event.id,
    customerId: event.customerId,
    eventType: event.eventType,
    createdAt: event.createdAt ? timestampDate(event.createdAt).toISOString() : undefined,
  };
}

function requireCustomer(customer: ProtoCustomer | undefined): ProtoCustomer {
  if (!customer) throw new ConnectError(t('customer.api.missingResponse'), Code.Internal);
  return customer;
}

export async function listCustomers(includeDisabled = false): Promise<CustomerSummary[]> {
  const response = await client.listCustomers({ includeDisabled });
  return response.customers.map(mapCustomer);
}

export async function getCustomer(customerId: string): Promise<CustomerSummary> {
  const response = await client.getCustomer({ customerId });
  return mapCustomer(requireCustomer(response.customer));
}

export async function createCustomer(input: CustomerFormInput): Promise<CustomerSummary> {
  const response = await client.createCustomer({ name: input.name.trim(), slug: input.slug.trim() });
  return mapCustomer(requireCustomer(response.customer));
}

export async function updateCustomer(customerId: string, input: CustomerFormInput): Promise<CustomerSummary> {
  const response = await client.updateCustomer({
    customerId,
    name: input.name.trim(),
    slug: input.slug.trim(),
    expectedVersion: BigInt(input.version),
  });
  return mapCustomer(requireCustomer(response.customer));
}

export async function disableCustomer(customerId: string): Promise<void> {
  await client.disableCustomer({ customerId });
}

export async function listCustomerEvents(customerId: string): Promise<CustomerEvent[]> {
  const response = await client.listCustomerEvents({ customerId });
  return response.events.map(mapEvent);
}
const ERROR_CODE_NAMES: Partial<Record<Code, string>> = {
  [Code.PermissionDenied]: 'permission_denied',
  [Code.NotFound]: 'not_found',
  [Code.Aborted]: 'optimistic_lock_conflict',
};

export function mapSaveError(error: unknown): SaveError {
  const connectError = ConnectError.from(error);
  if (connectError.code === Code.Unavailable) {
    return { code: 'network_error', message: t('customer.api.connectFailed') };
  }

  const rawMessage = connectError.rawMessage;
  const messageCode = rawMessage.match(/^(optimistic_lock_conflict|customer_disabled|permission_denied|not_found):/)?.[1];
  return {
    code: messageCode || ERROR_CODE_NAMES[connectError.code] || 'unknown',
    message: rawMessage || t('customer.api.requestFailed'),
  };
}
