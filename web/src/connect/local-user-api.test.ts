import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it, vi } from 'vitest';
import { createLocalUser, mapLocalUserError, LOCAL_USER_ROLES } from './local-user-api';
import { authClient } from './client';
import { t } from '@/i18n/messages';
import { statusLabel } from '@/i18n/status-labels';

/** A ConnectError carrying the headers the server sets (see error-copy.ts). */
function failure(code: Code, headers: Record<string, string> = {}): ConnectError {
  const metadata = new Headers();
  for (const [name, value] of Object.entries(headers)) metadata.set(name, value);
  return new ConnectError('server said something', code, metadata);
}

describe('mapLocalUserError', () => {
  // The whole namespace is adminOnly, so a non-admin must get a stable refusal.
  it('classifies a permission denial', () => {
    const mapped = mapLocalUserError(failure(Code.PermissionDenied));

    expect(mapped.code).toBe('permission_denied');
    expect(mapped.message).toContain('platform_admin');
    expect(mapped.retryable).toBe(false);
  });

  // A stale cursor is INVALID_ARGUMENT too, but it must be treated differently: the
  // store reloads page one instead of retrying the same token.
  it('separates a stale cursor from other invalid input', () => {
    expect(mapLocalUserError(failure(Code.InvalidArgument, { 'X-Reason-Code': 'invalid_cursor' })).code).toBe(
      'invalid_cursor',
    );
    expect(mapLocalUserError(failure(Code.InvalidArgument)).code).toBe('invalid_input');
  });

  it('prefers the shared copy for a known reason code and keeps the correlation data', () => {
    const mapped = mapLocalUserError(
      failure(Code.InvalidArgument, { 'X-Reason-Code': 'invalid_cursor', 'X-Request-ID': 'req-9' }),
    );

    // The catalog owns the copy for a known reason code, and the technical line keeps
    // both correlation values (the code and the request id).
    expect(mapped.message).toBe(t('reason.invalid_cursor'));
    expect(mapped.details).toContain('invalid_cursor');
    expect(mapped.details).toContain('req-9');
    expect(mapped.details).toContain('InvalidArgument');
  });

  // An expired session is not an outage: the user must sign in again, not retry.
  it('classifies an expired session instead of reporting an outage', () => {
    const mapped = mapLocalUserError(failure(Code.Unauthenticated));

    expect(mapped.code).toBe('session_expired');
    expect(mapped.retryable).toBe(false);
  });

  it('classifies a disabled organization', () => {
    expect(mapLocalUserError(failure(Code.FailedPrecondition)).code).toBe('org_disabled');
  });

  // The page pre-checks bcrypt's 72-byte limit, so an Internal answer is the server's
  // own failure — reporting it as a rejected password would hide an outage AND disable
  // the retry button (an independent review caught exactly that in the previous revision).
  it('treats an internal answer as a retryable outage, not a password verdict', () => {
    const mapped = mapLocalUserError(failure(Code.Internal));

    expect(mapped.code).toBe('unavailable');
    expect(mapped.retryable).toBe(true);
  });

  // The server sets X-Reason-Code, and the context keeps the branch alive without it.
  it('recognises a stale cursor from the paging context', () => {
    expect(mapLocalUserError(failure(Code.InvalidArgument), 'loadMore').code).toBe('invalid_cursor');
    expect(mapLocalUserError(failure(Code.InvalidArgument), 'list').code).toBe('invalid_input');
  });

  it('marks an outage as retryable', () => {
    expect(mapLocalUserError(failure(Code.Unavailable)).retryable).toBe(true);
  });
});

describe('grantable roles', () => {
  // D-16 rejects platform_admin here, and the console must not offer it.
  it('never offers platform_admin', () => {
    expect(LOCAL_USER_ROLES).toEqual(['viewer', 'deployer', 'release_admin']);
  });
});

/*
 * A CreateLocalUser response without the user message left the wrapper to synthesise
 * one field by field, and it filled the status in as `active` — which, per
 * api/proto/auth/v1/auth.proto, is the claim that the account can authenticate.
 * Whatever the reason the message is missing, that is not something the console knows.
 */
describe('createLocalUser without the user message', () => {
  it('does not claim the account is active', async () => {
    vi.spyOn(authClient, 'createLocalUser').mockResolvedValue({} as never);

    const created = await createLocalUser({ username: 'ada', password: 'correct-horse', role: 'viewer' });

    expect(created.status).toBe('unknown');
    expect(created.status).not.toBe('active');
    // The label is what the page renders, so `unknown` must not leak through raw.
    expect(statusLabel('localUser', created.status)).toBe('未知');
  });

  // Positive control: a response that DOES carry the user keeps its status.
  it('keeps the status the server did send', async () => {
    vi.spyOn(authClient, 'createLocalUser').mockResolvedValue({
      user: { id: 'u-1', username: 'ada', roles: ['viewer'], orgId: 'org-1', status: 'pending' },
    } as never);

    const created = await createLocalUser({ username: 'ada', password: 'correct-horse', role: 'viewer' });

    expect(created.status).toBe('pending');
  });
});
