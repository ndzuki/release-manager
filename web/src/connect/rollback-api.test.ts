import { Code, ConnectError } from '@connectrpc/connect';
import { create, toBinary } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';
import { CreateOperationGateDetailSchema } from '@/gen/orchestrator/v1/orchestrator_pb';
import { mapRollbackError } from './rollback-api';

/*
 * The mapper is the part of the rollback UI that decides what an operator is told
 * and whether refreshing can help, so every carrier the server uses is pinned here:
 * typed gate detail, X-Reason-Code metadata, and the leading machine token.
 */
function refused(message: string, code: Code, metadata?: Record<string, string>, gate?: { convergenceTaskIds?: string[]; unresolvedOperationIds?: string[] }) {
  const error = new ConnectError(message, code, metadata);
  if (gate) {
    error.details = [
      {
        type: CreateOperationGateDetailSchema.typeName,
        value: toBinary(
          CreateOperationGateDetailSchema,
          create(CreateOperationGateDetailSchema, {
            convergenceTaskIds: gate.convergenceTaskIds ?? [],
            unresolvedOperationIds: gate.unresolvedOperationIds ?? [],
          }),
        ),
      },
    ] as never;
  }
  return error;
}

describe('mapRollbackError', () => {
  it('reads a convergence gate from the typed detail, not from the message', () => {
    const failure = mapRollbackError(
      refused('refused', Code.FailedPrecondition, undefined, { convergenceTaskIds: ['task-1'] }),
    );

    expect(failure.code).toBe('convergence_pending');
    expect(failure.typed).toBe(true);
    expect(failure.message).toContain('收敛');
  });

  it('reads an unresolved emergency effect from the typed detail', () => {
    const failure = mapRollbackError(
      refused('refused', Code.FailedPrecondition, undefined, { unresolvedOperationIds: ['op-1'] }),
    );

    expect(failure.code).toBe('emergency_unresolved');
    expect(failure.typed).toBe(true);
  });

  it('uses the stable X-Reason-Code for a busy release', () => {
    const failure = mapRollbackError(
      refused('release_busy: definition has active operation', Code.FailedPrecondition, { 'X-Reason-Code': 'release_busy' }),
    );

    expect(failure.code).toBe('release_busy');
    expect(failure.typed).toBe(true);
    // retrying while the release is busy does not help; refreshing later does
    // (mapOperationError maps release_busy as non-retryable too).
    expect(failure.retryable).toBe(false);
  });

  // Only a moved revision is fixed by refreshing; the rest are not retryable.
  it.each([
    ['revision_conflict: expected revision 5, but current revision is 3', 'stale_revision', true],
    ['release_convergence_pending', 'convergence_pending', false],
    ['emergency_effect_unresolved', 'emergency_unresolved', false],
    ['release_definition_disabled: definition def-1 is disabled', 'definition_disabled', false],
    ['release_not_found: no installed release for definition def-1', 'release_not_found', false],
    ['release_busy: definition has active operation', 'release_busy', false],
  ])('classifies %s as %s', (message, code, retryable) => {
    const failure = mapRollbackError(refused(message, Code.FailedPrecondition));

    expect(failure.code).toBe(code);
    expect(failure.retryable).toBe(retryable);
    expect(failure.typed).toBe(false);
  });

  it('does not mistake an unrelated precondition for a stale revision', () => {
    const failure = mapRollbackError(refused('something_new_we_never_saw', Code.FailedPrecondition));

    expect(failure.code).toBe('unavailable');
    expect(failure.message).not.toContain('刷新');
  });

  it('maps an idempotency conflict to a non-retryable conflict', () => {
    const failure = mapRollbackError(
      refused('idempotency_conflict: key already used with different request', Code.AlreadyExists),
    );

    expect(failure.code).toBe('idempotency_conflict');
    expect(failure.retryable).toBe(false);
  });

  it.each([
    [Code.NotFound, 'not_found'],
    [Code.PermissionDenied, 'permission_denied'],
  ])('maps %s to %s', (code, expected) => {
    expect(mapRollbackError(refused('nope', code)).code).toBe(expected);
  });

  it('distinguishes the required-reason refusal from a bad revision', () => {
    expect(mapRollbackError(refused('reason is required for rollback', Code.InvalidArgument)).code).toBe('reason_required');
    expect(mapRollbackError(refused('target_revision 5 must be < expected_current_revision 3', Code.InvalidArgument)).code).toBe('revision_invalid');
    expect(mapRollbackError(refused('rollback_values_not_allowed', Code.InvalidArgument)).code).toBe('revision_invalid');
  });
});
