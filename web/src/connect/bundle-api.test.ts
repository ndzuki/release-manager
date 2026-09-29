import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it } from 'vitest';
import { BundleStatus } from '@/gen/common/v1/domain_pb';
import { mapBundleError, statusLabel } from './bundle-api';

describe('bundle status labels', () => {
  it.each([
    [BundleStatus.RECEIVED, '已接收'],
    [BundleStatus.VALIDATED, '已校验'],
    [BundleStatus.REJECTED, '已拒绝'],
    [BundleStatus.ARCHIVED, '已归档'],
  ])('labels %s', (status, label) => {
    expect(statusLabel(status)).toBe(label);
  });

  it('does not call an unknown status empty', () => {
    expect(statusLabel(BundleStatus.UNSPECIFIED)).toBe('未知');
  });
});

describe('mapBundleError', () => {
  it('keeps a permission refusal distinct from an empty page', () => {
    const failure = mapBundleError(new ConnectError('nope', Code.PermissionDenied));

    expect(failure.code).toBe('permission_denied');
    expect(failure.retryable).toBe(false);
    expect(failure.message).toContain('不是「没有数据」');
  });

  // The bundle service sets no X-Reason-Code, so the mapper must not invent one: any
  // InvalidArgument is a retryable input problem and the store drops the cursor.
  it('maps every invalid argument to a retryable input problem', () => {
    const failure = mapBundleError(new ConnectError('invalid page token', Code.InvalidArgument));

    expect(failure.code).toBe('invalid_input');
    expect(failure.retryable).toBe(true);
  });

  it('does not read free text to guess a stale cursor', () => {
    // A message mentioning a cursor must not change the classification.
    const withCursorWords = mapBundleError(new ConnectError('cursor cursor token', Code.InvalidArgument));

    expect(withCursorWords.code).toBe('invalid_input');
  });

  it.each([
    [Code.NotFound, 'not_found'],
    [Code.Unavailable, 'unavailable'],
    [Code.Internal, 'unavailable'],
  ])('maps %s to %s', (code, expected) => {
    expect(mapBundleError(new ConnectError('x', code)).code).toBe(expected);
  });

  it('carries the correlation line for the technical details', () => {
    const failure = mapBundleError(new ConnectError('x', Code.Internal, { 'X-Request-ID': 'req-7' }));

    expect(failure.details).toContain('requestId=req-7');
  });
});
