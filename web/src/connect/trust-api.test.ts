import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it } from 'vitest';
import { allowedActions, isLive, mapTrustError } from './trust-api';

function refused(message: string, code: Code, metadata?: Record<string, string>) {
  return new ConnectError(message, code, metadata);
}

describe('mapTrustError', () => {
  /*
   * internal/trust/types.go defines the two stable machine tokens; the mapper must
   * read them rather than free text, and a state refusal (no token) is its own code.
   */
  it.each([
    ['overlap_conflict', 'overlap_conflict', true],
    ['last_root_removal_forbidden', 'last_live_root', true],
  ])('reads the %s token as %s', (message, code, typed) => {
    const failure = mapTrustError(refused(message, Code.InvalidArgument));

    expect(failure.code).toBe(code);
    expect(failure.typed).toBe(typed);
  });

  it('reads the stable X-Reason-Code variants too', () => {
    expect(mapTrustError(refused('nope', Code.FailedPrecondition, { 'X-Reason-Code': 'overlap_conflict' })).code).toBe('overlap_conflict');
    expect(mapTrustError(refused('nope', Code.FailedPrecondition, { 'X-Reason-Code': 'last_root_removal_forbidden' })).code).toBe('last_live_root');
  });

  // Same contract as above: catalog copy wins (the local wording says "无法解析").
  it('prefers the centralised copy for authentication_required', () => {
    const failure = mapTrustError(refused('nope', Code.Unauthenticated, { 'X-Reason-Code': 'authentication_required' }));

    expect(failure.message).toContain('会话已失效');
  });

  it('reports a bare state refusal as a state conflict', () => {
    const failure = mapTrustError(refused('root is not active', Code.FailedPrecondition));

    expect(failure.code).toBe('state_conflict');
    expect(failure.typed).toBe(false);
    expect(failure.message).toContain('状态');
  });

  it.each([
    [Code.NotFound, 'not_found'],
    [Code.PermissionDenied, 'permission_denied'],
    [Code.InvalidArgument, 'invalid_input'],
    [Code.Internal, 'unavailable'],
  ])('maps %s to %s', (code, expected) => {
    expect(mapTrustError(refused('whatever', code)).code).toBe(expected);
  });
});

describe('trust root state machine helpers', () => {
  // Mirrors the RPC comments: ACTIVE rotates/retires/revokes, GRACE ends its window.
  it.each([
    ['active', ['retire', 'revoke']],
    ['grace', ['end_grace', 'retire', 'revoke']],
    ['pending', []],
    ['retired', []],
    ['revoked', []],
    ['unspecified', []],
  ])('offers %s -> %j', (state, actions) => {
    expect(allowedActions(state as never)).toEqual(actions);
  });

  // The server's own definition (internal/store/store.go): only active and grace
  // count as live. Counting pending would loosen the last-root guard.
  it('counts only active and grace as live', () => {
    expect(['active', 'grace'].map((s) => isLive(s as never))).toEqual([true, true]);
    expect(['pending', 'retired', 'revoked', 'unspecified'].map((s) => isLive(s as never))).toEqual([false, false, false, false]);
  });
});
