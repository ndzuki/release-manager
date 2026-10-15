import { fromBinary } from '@bufbuild/protobuf';
import { ConnectError } from '@connectrpc/connect';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  CreateOrganizationRequestSchema,
  DisableOrganizationRequestSchema,
  GetOrganizationRequestSchema,
  UpdateOrganizationRequestSchema,
  type CreateOrganizationRequest,
  type DisableOrganizationRequest,
  type GetOrganizationRequest,
  type UpdateOrganizationRequest,
} from '@/gen/auth/v1/auth_pb';
import {
  createOrganization,
  disableOrganization,
  getOrganization,
  updateOrganization,
} from './organization-api';

/*
 * Wire-shape tests for the organization-level writes (TASK-301).
 *
 * The store and page tests inject the whole `organization-api` module, and the unit
 * tests spy on the client METHOD, so nothing guarded the two things the transport
 * actually carries: which procedure path is called and what the binary body holds.
 * A swapped procedure name or a dropped `expected_version` would have stayed green
 * while every real create/disable failed.
 *
 * These go through the REAL client (binary format) with a stubbed fetch, and decode
 * the body with the same schema the Go handler uses. The stub answers 500 because the
 * request is the subject here — the response mapping is asserted in
 * organization-api.test.ts.
 */
function stubFetch(body: unknown, status = 500) {
  const calls: Array<{ url: string; bytes: Uint8Array }> = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = new Request(input as RequestInfo, init);
      calls.push({ url: request.url, bytes: new Uint8Array(await request.arrayBuffer()) });
      return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    }),
  );
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('createOrganization wire shape', () => {
  it('posts a trimmed name to CreateOrganization as a binary body', async () => {
    const calls = stubFetch({ code: 'internal', message: 'boom' });

    await expect(createOrganization('  Acme  ')).rejects.toBeInstanceOf(ConnectError);

    expect(calls).toHaveLength(1);
    expect(calls[0]!.url).toContain('/auth.v1.OrganizationService/CreateOrganization');
    const sent: CreateOrganizationRequest = fromBinary(CreateOrganizationRequestSchema, calls[0]!.bytes);
    expect(sent.name).toBe('Acme');
  });
});

describe('getOrganization wire shape', () => {
  it('sends the organization id to GetOrganization', async () => {
    const calls = stubFetch({ code: 'not_found', message: 'organization not found' }, 404);

    await expect(getOrganization('org-1')).rejects.toBeInstanceOf(ConnectError);

    expect(calls).toHaveLength(1);
    expect(calls[0]!.url).toContain('/auth.v1.OrganizationService/GetOrganization');
    const sent: GetOrganizationRequest = fromBinary(GetOrganizationRequestSchema, calls[0]!.bytes);
    expect(sent.orgId).toBe('org-1');
  });
});

describe('updateOrganization wire shape', () => {
  it('carries the new name and the expected_version the caller read', async () => {
    const calls = stubFetch({ code: 'aborted', message: 'optimistic lock conflict' }, 409);

    await expect(updateOrganization('org-1', 'Renamed', 3n)).rejects.toBeInstanceOf(ConnectError);

    expect(calls).toHaveLength(1);
    expect(calls[0]!.url).toContain('/auth.v1.OrganizationService/UpdateOrganization');
    const sent: UpdateOrganizationRequest = fromBinary(UpdateOrganizationRequestSchema, calls[0]!.bytes);
    expect(sent.orgId).toBe('org-1');
    expect(sent.name).toBe('Renamed');
    // Without this field the server would compare-and-swap against 0 and refuse.
    expect(sent.expectedVersion).toBe(3n);
  });
});

describe('disableOrganization wire shape', () => {
  it('carries the expected_version DisableOrganization compares against', async () => {
    const calls = stubFetch({ code: 'aborted', message: 'optimistic lock conflict' }, 409);

    await expect(disableOrganization('org-1', 7n)).rejects.toBeInstanceOf(ConnectError);

    expect(calls).toHaveLength(1);
    expect(calls[0]!.url).toContain('/auth.v1.OrganizationService/DisableOrganization');
    const sent: DisableOrganizationRequest = fromBinary(DisableOrganizationRequestSchema, calls[0]!.bytes);
    expect(sent.orgId).toBe('org-1');
    expect(sent.expectedVersion).toBe(7n);
  });
});
