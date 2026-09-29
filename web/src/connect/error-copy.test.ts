import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it } from 'vitest';
import { correlationLine, copyForReason, describeError } from './error-copy';

function failed(code: Code, metadata?: Record<string, string>) {
  return new ConnectError('whatever', code, metadata);
}

describe('describeError', () => {
  it('reads the stable reason code and the request id', () => {
    const described = describeError(failed(Code.FailedPrecondition, {
      'X-Reason-Code': 'release_busy',
      'X-Request-ID': 'req-42',
    }));

    expect(described.reasonCode).toBe('release_busy');
    expect(described.requestId).toBe('req-42');
    expect(described.code).toBe(Code.FailedPrecondition);
  });

  // 20+ procedures set X-Reason-Code; free text is never matched.
  it('maps a known reason code to centralised copy', () => {
    const described = describeError(failed(Code.FailedPrecondition, { 'X-Reason-Code': 'last_root_removal_forbidden' }));

    expect(described.message).toContain('最后一个可用信任根');
  });

  it('has no copy for an unknown reason code, so callers keep their own wording', () => {
    const described = describeError(failed(Code.Internal, { 'X-Reason-Code': 'something_new' }));

    expect(described.message).toBeNull();
    expect(described.reasonCode).toBe('something_new');
  });

  it('reports no reason code when the server sent none', () => {
    const described = describeError(failed(Code.Unavailable));

    expect(described.reasonCode).toBe('');
    expect(described.message).toBeNull();
    expect(described.requestId).toBe('');
  });

  it('marks only transient codes retryable', () => {
    expect(describeError(failed(Code.Unavailable)).retryable).toBe(true);
    expect(describeError(failed(Code.DeadlineExceeded)).retryable).toBe(true);
    expect(describeError(failed(Code.ResourceExhausted)).retryable).toBe(true);
    expect(describeError(failed(Code.Aborted)).retryable).toBe(true);
    expect(describeError(failed(Code.PermissionDenied)).retryable).toBe(false);
    expect(describeError(failed(Code.NotFound)).retryable).toBe(false);
  });
});

describe('correlationLine', () => {
  it('carries code, procedure, reason and request id', () => {
    const described = describeError(failed(Code.Aborted, {
      'X-Reason-Code': 'optimistic_lock_conflict',
      'X-Request-ID': 'req-9',
    }));

    expect(correlationLine(described, '/auth.v1.OrganizationService/UpdateMemberRole')).toBe(
      'code=Aborted · procedure=/auth.v1.OrganizationService/UpdateMemberRole · reason=optimistic_lock_conflict · requestId=req-9',
    );
  });

  it('omits what the server did not send', () => {
    expect(correlationLine(describeError(failed(Code.Internal)))).toBe('code=Internal');
  });
});

describe('copyForReason', () => {
  it('returns null for a code the catalog does not know', () => {
    expect(copyForReason('nope_not_here')).toBeNull();
    expect(copyForReason('invalid_cursor')).toContain('页码');
  });
});
