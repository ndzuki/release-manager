import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it } from 'vitest';
import { mapCleanupError } from './cleanup-api';

function failed(message: string, code: Code, metadata?: Record<string, string>) {
  return new ConnectError(message, code, metadata);
}

describe('cleanup error mapping', () => {
  // CleanupService sets no X-Reason-Code; its refusals are documented machine tokens
  // in the message, matched EXACTLY (never as free-text substrings).
  it('reads the documented already-requested token', () => {
    const failure = mapCleanupError(failed('cleanup_already_requested', Code.AlreadyExists));

    expect(failure.code).toBe('already_requested');
    expect(failure.retryable).toBe(false);
    expect(failure.message).toContain('24 小时');
  });

  // This one DISCRIMINATES the exact-token branch from the code fallback: the
  // in-flight token maps to `already_running` while AlreadyExists alone would say
  // `already_requested` (the earlier version of this test could not tell them apart).
  it('separates the in-flight token from the key-reuse token', () => {
    const inFlight = mapCleanupError(failed('cleanup is already in progress', Code.AlreadyExists));
    const reused = mapCleanupError(failed('cleanup_already_requested', Code.AlreadyExists));

    expect(inFlight.code).toBe('already_running');
    expect(reused.code).toBe('already_requested');
  });

  it('reads the concurrent-run refusal', () => {
    const failure = mapCleanupError(failed('cleanup already running', Code.ResourceExhausted));

    expect(failure.code).toBe('already_running');
    expect(failure.retryable).toBe(true);
  });

  it('reads the maintenance refusal', () => {
    expect(mapCleanupError(failed('maintenance', Code.Unavailable)).code).toBe('maintenance');
  });

  it('does not turn any other unavailable into a maintenance refusal', () => {
    expect(mapCleanupError(failed('upstream exploded', Code.Unavailable)).code).toBe('unavailable');
  });

  it.each([
    [Code.NotFound, 'not_found'],
    [Code.FailedPrecondition, 'unrestorable'],
    [Code.PermissionDenied, 'permission_denied'],
    [Code.InvalidArgument, 'invalid_input'],
    [Code.Internal, 'unavailable'],
  ])('maps %s to %s', (code, expected) => {
    expect(mapCleanupError(failed('x', code)).code).toBe(expected);
  });

  it('honours a legacy X-Reason-Code as well', () => {
    expect(mapCleanupError(failed('x', Code.AlreadyExists, { 'X-Reason-Code': 'cleanup_already_requested' })).code).toBe('already_requested');
  });

  it('carries the correlation line', () => {
    expect(mapCleanupError(failed('x', Code.Internal, { 'X-Request-ID': 'req-5' })).details).toContain('requestId=req-5');
  });
});
