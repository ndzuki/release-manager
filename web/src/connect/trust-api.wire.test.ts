import { ConnectError } from '@connectrpc/connect';
import { fromBinary } from '@bufbuild/protobuf';
import { timestampDate } from '@bufbuild/protobuf/wkt';
import {
  CreateTrustRootRequestSchema,
  RotateTrustRootRequestSchema,
  type CreateTrustRootRequest,
  type RotateTrustRootRequest,
} from '@/gen/trust/v1/trust_pb';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createTrustRoot, rotateTrustRoot } from './trust-api';

/*
 * Wire-shape tests for the trust-root write seam (TASK-280 D11).
 *
 * The page and store tests inject the whole trust-api module, so nothing guarded the
 * mapping from the input object to the Connect message: dropping public_key_pem or
 * old_root_id would have looked perfectly green while every real rotation failed
 * server-side. These go through the REAL client with a stubbed fetch and decode the
 * binary body with the same schema the Go handler uses, so the assertion is on the
 * bytes the backend would receive — not on a mock of the input.
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

const PUBLIC_PEM = '-----BEGIN PUBLIC KEY-----\nMCowBQYDK2VwAyEAGb9ECWmEzf6FQbrBZ9w7lshQhqowtrbLDFw4rXAxZuE=\n-----END PUBLIC KEY-----';

const VALID_FROM = new Date('2026-10-11T08:00:00Z');
const GRACE_UNTIL = new Date('2026-10-12T08:00:00Z');

const CREATE_INPUT = {
  environment: 'staging',
  keyId: 'key-2026-10',
  publicKeyPem: PUBLIC_PEM,
  issuer: 'CN=release-signer',
  subjectPattern: 'release-manager',
  operator: 'dev-admin',
  validFrom: VALID_FROM,
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('createTrustRoot wire shape', () => {
  it('sends every CreateTrustRoot field, public key included', async () => {
    const calls = stubFetch({ code: 'internal', message: 'boom' });

    await expect(createTrustRoot(CREATE_INPUT)).rejects.toBeInstanceOf(ConnectError);

    expect(calls).toHaveLength(1);
    expect(calls[0]!.url).toContain('/trust.v1.TrustService/CreateTrustRoot');
    const sent: CreateTrustRootRequest = fromBinary(CreateTrustRootRequestSchema, calls[0]!.bytes);
    expect(sent.environment).toBe('staging');
    expect(sent.keyId).toBe('key-2026-10');
    expect(sent.publicKeyPem).toBe(PUBLIC_PEM);
    expect(sent.issuer).toBe('CN=release-signer');
    expect(sent.subjectPattern).toBe('release-manager');
    expect(sent.operator).toBe('dev-admin');
    expect(timestampDate(sent.validFrom!).toISOString()).toBe(VALID_FROM.toISOString());
  });
});

describe('rotateTrustRoot wire shape', () => {
  it('carries the old root id, the new public key and the grace window', async () => {
    const calls = stubFetch({ code: 'internal', message: 'boom' });

    await expect(
      rotateTrustRoot({ ...CREATE_INPUT, oldRootId: 'root-old', graceUntil: GRACE_UNTIL }),
    ).rejects.toBeInstanceOf(ConnectError);

    expect(calls).toHaveLength(1);
    expect(calls[0]!.url).toContain('/trust.v1.TrustService/RotateTrustRoot');
    const sent: RotateTrustRootRequest = fromBinary(RotateTrustRootRequestSchema, calls[0]!.bytes);
    expect(sent.environment).toBe('staging');
    expect(sent.oldRootId).toBe('root-old');
    expect(sent.keyId).toBe('key-2026-10');
    expect(sent.publicKeyPem).toBe(PUBLIC_PEM);
    expect(sent.issuer).toBe('CN=release-signer');
    expect(sent.subjectPattern).toBe('release-manager');
    expect(sent.operator).toBe('dev-admin');
    expect(timestampDate(sent.validFrom!).toISOString()).toBe(VALID_FROM.toISOString());
    expect(timestampDate(sent.graceUntil!).toISOString()).toBe(GRACE_UNTIL.toISOString());
  });
});

/*
 * The private half must never leave the browser (internal/trust/types.go:112-114
 * refuses it server-side too). This is asserted as "no request at all", not as an
 * error shape: a refused call must not put the material on the wire first.
 */
describe('private key material', () => {
  it.each([
    ['create', () => createTrustRoot({ ...CREATE_INPUT, publicKeyPem: '-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----' })],
    ['rotate', () => rotateTrustRoot({ ...CREATE_INPUT, oldRootId: 'root-old', publicKeyPem: '-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----' })],
  ])('refuses to send a %s request containing a private key', async (_label, call) => {
    const calls = stubFetch({ code: 'internal', message: 'boom' });

    await expect(call()).rejects.toBeInstanceOf(ConnectError);

    expect(calls).toHaveLength(0);
  });
});
