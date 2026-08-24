import { create, toBinary } from '@bufbuild/protobuf';
import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it } from 'vitest';
import {
  EmergencyErrorDetailSchema,
  EmergencyReasonCode,
} from '@/gen/orchestrator/v1/orchestrator_pb';
import { mapEmergencyError, reasonMessageFor } from '@/features/emergency/errors';

function typedError(reasonCode: EmergencyReasonCode, message: string, retryable: boolean): ConnectError {
  const error = new ConnectError(`[failed_precondition] ${message}`, Code.FailedPrecondition);
  error.details.push({
    type: EmergencyErrorDetailSchema.typeName,
    value: toBinary(
      EmergencyErrorDetailSchema,
      create(EmergencyErrorDetailSchema, { reasonCode, message, retryable }),
    ),
  });
  return error;
}

describe('mapEmergencyError', () => {
  it('decodes the typed EmergencyErrorDetail via findDetails (core/go/connect-rpc.md)', () => {
    const mapped = mapEmergencyError(
      typedError(EmergencyReasonCode.KILL_SWITCH_DISABLED, 'emergency change is disabled', false),
    );
    expect(mapped).toEqual({
      code: 'kill_switch_disabled',
      message: '紧急变更已被平台开关禁用',
      retryable: false,
      typed: true,
    });
  });

  it('honors the detail retryable flag', () => {
    const mapped = mapEmergencyError(typedError(EmergencyReasonCode.OPERATION_IN_PROGRESS, 'busy', true));
    expect(mapped).toMatchObject({ code: 'operation_in_progress', retryable: true, typed: true });
  });

  it('falls back to the X-Reason-Code metadata header for legacy codes', () => {
    const error = new ConnectError('stale snapshot', Code.Unavailable, new Headers({ 'X-Reason-Code': 'authorization_snapshot_stale' }));
    const mapped = mapEmergencyError(error);
    expect(mapped).toMatchObject({ code: 'authorization_snapshot_stale', retryable: true, typed: false });
    expect(mapped.message).toContain('授权');
  });

  it('maps Connect codes without any stable reason', () => {
    expect(mapEmergencyError(new ConnectError('denied', Code.PermissionDenied))).toMatchObject({
      code: 'permission_denied',
      typed: false,
    });
    expect(mapEmergencyError(new ConnectError('gone', Code.NotFound))).toMatchObject({
      code: 'not_found',
      retryable: false,
    });
    expect(mapEmergencyError(new ConnectError('bad', Code.InvalidArgument))).toMatchObject({
      code: 'invalid_argument',
      retryable: false,
    });
    expect(mapEmergencyError(new ConnectError('down', Code.Unavailable))).toMatchObject({
      code: 'network_error',
      retryable: true,
    });
    expect(mapEmergencyError(new ConnectError('slow', Code.DeadlineExceeded))).toMatchObject({
      code: 'network_error',
      retryable: true,
    });
    expect(mapEmergencyError(new ConnectError('dup', Code.AlreadyExists))).toMatchObject({
      code: 'idempotency_conflict',
      retryable: false,
    });
  });

  it('never parses the free-text message as a contract', () => {
    const error = new ConnectError('do not trust this text', Code.Unknown);
    const mapped = mapEmergencyError(error);
    expect(mapped.code).toBe('unknown');
    expect(mapped.message).toBe('do not trust this text');
  });

  it('maps every typed EmergencyReasonCode to a stable message', () => {
    const codes: Array<[EmergencyReasonCode, string]> = [
      [EmergencyReasonCode.KILL_SWITCH_DISABLED, 'kill_switch_disabled'],
      [EmergencyReasonCode.NO_CANDIDATE_ARTIFACT, 'no_candidate_artifact'],
      [EmergencyReasonCode.VALUES_CONFLICT, 'values_conflict'],
      [EmergencyReasonCode.OPERATION_IN_PROGRESS, 'operation_in_progress'],
      [EmergencyReasonCode.INTERNAL, 'internal'],
      [EmergencyReasonCode.ARTIFACT_NOT_FOUND, 'artifact_not_found'],
      [EmergencyReasonCode.WORKLOAD_NOT_FOUND, 'workload_not_found'],
      [EmergencyReasonCode.CONTAINER_NOT_FOUND, 'container_not_found'],
      [EmergencyReasonCode.VERSION_INVALID, 'version_invalid'],
      [EmergencyReasonCode.LOCKED_PATH, 'locked_path'],
    ];
    for (const [reasonCode, expected] of codes) {
      expect(mapEmergencyError(typedError(reasonCode, 'x', false)).code).toBe(expected);
    }
  });

  it('provides messages for stable codes and a fallback otherwise', () => {
    expect(reasonMessageFor('network_error', 'fallback')).toBe('网络错误，请检查连接后重试');
    expect(reasonMessageFor('made_up_code', 'fallback')).toBe('fallback');
  });
});
