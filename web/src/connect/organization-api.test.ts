import { create, fromBinary, toBinary, type MessageInitShape } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it, vi } from 'vitest';
import {
  CreateOrganizationRequestSchema,
  DisableOrganizationRequestSchema,
  OrganizationSchema,
  UpdateOrganizationRequestSchema,
  type CreateOrganizationRequest,
  type DisableOrganizationRequest,
  type GetOrganizationRequest,
  type Organization,
  type UpdateOrganizationRequest,
} from '@/gen/auth/v1/auth_pb';
import { organizationClient } from './client';
import {
  createOrganization,
  disableOrganization,
  getOrganization,
  mapOrganizationError,
  updateOrganization,
} from './organization-api';

/*
 * Organization-level writes (REQ-026). The member wrappers next to them are covered
 * through the store and page tests; these cases exist because the four writes were
 * missing entirely, so nothing else pins the request the server actually receives.
 *
 * The transport is BINARY (client.ts sets `useBinaryFormat: true`), so a plain
 * in-memory object is not evidence that the request survives serialization: the
 * cases below round-trip the built message through `toBinary`/`fromBinary` and assert
 * the decoded fields, which is the codec the transport itself uses.
 */

function protoOrganization(overrides: MessageInitShape<typeof OrganizationSchema> = {}): Organization {
  return create(OrganizationSchema, {
    id: 'org-1',
    name: 'Platform',
    status: 'active',
    optimisticVersion: 3n,
    createdAt: timestampFromDate(new Date('2026-09-28T00:00:00Z')),
    updatedAt: timestampFromDate(new Date('2026-09-29T00:00:00Z')),
    ...overrides,
  });
}

function denied(message = 'requires role platform_admin') {
  return new ConnectError(message, Code.PermissionDenied);
}

describe('createOrganization', () => {
  it('trims the name, sends one CreateOrganization request and maps the new organization', async () => {
    const spy = vi
      .spyOn(organizationClient, 'createOrganization')
      .mockResolvedValue({ organization: protoOrganization({ name: 'Acme' }) } as never);

    const created = await createOrganization('  Acme  ');

    const request = spy.mock.calls[0]?.[0] as unknown as CreateOrganizationRequest;
    expect(request.$typeName).toBe(CreateOrganizationRequestSchema.typeName);
    expect(request.name).toBe('Acme');
    // The wire form is the contract: decode what the transport would have sent.
    const decoded = fromBinary(CreateOrganizationRequestSchema, toBinary(CreateOrganizationRequestSchema, request));
    expect(decoded.name).toBe('Acme');
    expect(toBinary(CreateOrganizationRequestSchema, request)).toHaveLength(6); // field 1, len 4, "Acme"

    expect(created).toEqual({
      id: 'org-1',
      name: 'Acme',
      status: 'active',
      optimisticVersion: 3n,
      createdAt: '2026-09-28T00:00:00.000Z',
      updatedAt: '2026-09-29T00:00:00.000Z',
    });
  });

  it('reports the organization as active whenever the wire status is not "disabled"', async () => {
    vi.spyOn(organizationClient, 'createOrganization').mockResolvedValue({
      // A future/unknown status must not be silently reported as disabled.
      organization: protoOrganization({ status: 'pending' }),
    } as never);

    expect((await createOrganization('Acme')).status).toBe('active');
  });

  /*
   * CreateOrganization is platform_admin-only (the handler re-checks the role on top
   * of the policy). The failure must reach the caller: the wrapper swallows nothing,
   * so the page can render the mapped reason instead of a false success.
   */
  it('propagates a PermissionDenied refusal without swallowing it', async () => {
    const failure = denied();
    vi.spyOn(organizationClient, 'createOrganization').mockRejectedValue(failure);

    await expect(createOrganization('Acme')).rejects.toBe(failure);
    const mapped = mapOrganizationError(failure);
    expect(mapped.code).toBe('permission_denied');
    expect(mapped.retryable).toBe(false);
  });
});

describe('getOrganization', () => {
  it('reads the current version of one organization', async () => {
    const spy = vi
      .spyOn(organizationClient, 'getOrganization')
      .mockResolvedValue({ organization: protoOrganization({ optimisticVersion: 7n }) } as never);

    const view = await getOrganization('org-1');

    const request = spy.mock.calls[0]?.[0] as unknown as GetOrganizationRequest;
    expect(request.orgId).toBe('org-1');
    expect(view.optimisticVersion).toBe(7n);
  });

  it('maps an unknown organization to a non-retryable not_found', async () => {
    const failure = new ConnectError('organization not found', Code.NotFound);
    vi.spyOn(organizationClient, 'getOrganization').mockRejectedValue(failure);

    await expect(getOrganization('org-9')).rejects.toBe(failure);
    const mapped = mapOrganizationError(failure);
    expect(mapped.code).toBe('not_found');
    expect(mapped.retryable).toBe(false);
  });
});

describe('updateOrganization', () => {
  it('sends the observed version as a bigint and returns the renamed organization', async () => {
    const spy = vi
      .spyOn(organizationClient, 'updateOrganization')
      .mockResolvedValue({ organization: protoOrganization({ name: 'Renamed', optimisticVersion: 4n }) } as never);

    const updated = await updateOrganization('org-1', ' Renamed ', 3n);

    const request = spy.mock.calls[0]?.[0] as unknown as UpdateOrganizationRequest;
    expect(request.$typeName).toBe(UpdateOrganizationRequestSchema.typeName);
    expect(request.orgId).toBe('org-1');
    expect(request.name).toBe('Renamed');
    expect(request.expectedVersion).toBe(3n);
    const decoded = fromBinary(UpdateOrganizationRequestSchema, toBinary(UpdateOrganizationRequestSchema, request));
    // Field 3 is expected_version: on the wire it must be the version we read.
    expect(decoded.expectedVersion).toBe(3n);
    expect(updated.name).toBe('Renamed');
    expect(updated.optimisticVersion).toBe(4n);
  });

  // Both writes carry a compare-and-swap token; a lost race is ABORTED, retryable
  // after re-reading. X-Reason-Code wins when the server sets one.
  it('maps a lost optimistic-lock race to a retryable conflict', async () => {
    const conflict = new ConnectError('', Code.Aborted, { 'X-Reason-Code': 'optimistic_lock_conflict' });
    vi.spyOn(organizationClient, 'updateOrganization').mockRejectedValue(conflict);

    await expect(updateOrganization('org-1', 'Renamed', 3n)).rejects.toBe(conflict);
    const mapped = mapOrganizationError(conflict);
    expect(mapped.code).toBe('conflict');
    expect(mapped.retryable).toBe(true);
  });

  it('maps a disabled organization to a non-retryable org_disabled', async () => {
    const disabled = new ConnectError('organization is disabled', Code.FailedPrecondition, {
      'X-Reason-Code': 'organization_disabled',
    });
    vi.spyOn(organizationClient, 'updateOrganization').mockRejectedValue(disabled);

    const mapped = mapOrganizationError(disabled);
    expect(mapped.code).toBe('org_disabled');
    expect(mapped.retryable).toBe(false);
  });
});

describe('disableOrganization', () => {
  it('sends the version and returns the disabled organization', async () => {
    const spy = vi
      .spyOn(organizationClient, 'disableOrganization')
      .mockResolvedValue({ organization: protoOrganization({ status: 'disabled' }) } as never);

    const disabled = await disableOrganization('org-1', 3n);

    const request = spy.mock.calls[0]?.[0] as unknown as DisableOrganizationRequest;
    expect(request.$typeName).toBe(DisableOrganizationRequestSchema.typeName);
    expect(request.orgId).toBe('org-1');
    expect(request.expectedVersion).toBe(3n);
    const decoded = fromBinary(
      DisableOrganizationRequestSchema,
      toBinary(DisableOrganizationRequestSchema, request),
    );
    expect(decoded.orgId).toBe('org-1');
    expect(decoded.expectedVersion).toBe(3n);
    expect(disabled.status).toBe('disabled');
  });

  /*
   * DisableOrganization's handler sets no reason code on a lost race — plain ABORTED
   * is the only carrier — so the code-based branch must classify it, not just the
   * X-Reason-Code one.
   */
  it('maps a bare Aborted answer to a retryable conflict', async () => {
    const aborted = new ConnectError('optimistic lock conflict', Code.Aborted);
    vi.spyOn(organizationClient, 'disableOrganization').mockRejectedValue(aborted);

    await expect(disableOrganization('org-1', 3n)).rejects.toBe(aborted);
    const mapped = mapOrganizationError(aborted);
    expect(mapped.code).toBe('conflict');
    expect(mapped.retryable).toBe(true);
  });

  it('maps an unknown organization to not_found and an internal fault to a retryable outage', async () => {
    expect(mapOrganizationError(new ConnectError('gone', Code.NotFound)).code).toBe('not_found');

    const outage = mapOrganizationError(new ConnectError('disable organization: boom', Code.Internal));
    expect(outage.code).toBe('unavailable');
    expect(outage.retryable).toBe(true);
  });

  // The technical line is what an operator hands to the server logs; the copy layer
  // must not replace it with payload data.
  it('carries the Connect code, reason code and request id in the technical line', () => {
    const mapped = mapOrganizationError(
      new ConnectError('', Code.Aborted, { 'X-Reason-Code': 'optimistic_lock_conflict', 'X-Request-ID': 'req-7' }),
    );

    expect(mapped.details).toContain('code=Aborted');
    expect(mapped.details).toContain('reason=optimistic_lock_conflict');
    expect(mapped.details).toContain('requestId=req-7');
  });
});
