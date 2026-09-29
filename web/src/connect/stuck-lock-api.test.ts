import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it } from 'vitest';
import { EmergencyAction } from '@/gen/orchestrator/v1/orchestrator_pb';
import { actionLabel, mapStuckLockError, mapStuckLockListError } from './stuck-lock-api';

function refused(code: Code, reason?: string) {
  return new ConnectError('whatever', code, reason ? { 'X-Reason-Code': reason } : undefined);
}

describe('stuck lock error mapping', () => {
  /*
   * emergencyError sets X-Reason-Code, so the mapper must read the stable header
   * rather than the free-text message.
   */
  it.each([
    ['reason_required', 'reason_required'],
    ['release_mode_unspecified', 'mode_required'],
    ['intent_not_found', 'not_found'],
    ['authentication_required', 'session_expired'],
  ])('reads X-Reason-Code %s as %s', (reason, expected) => {
    const failure = mapStuckLockError(refused(Code.InvalidArgument, reason));

    expect(failure.code).toBe(expected);
    expect(failure.typed).toBe(true);
  });

  it('falls back to the Connect code when no reason code is present', () => {
    expect(mapStuckLockError(refused(Code.InvalidArgument)).code).toBe('reason_required');
    expect(mapStuckLockError(refused(Code.NotFound)).code).toBe('not_found');
    expect(mapStuckLockError(refused(Code.PermissionDenied)).code).toBe('permission_denied');
    expect(mapStuckLockError(refused(Code.Unauthenticated)).code).toBe('session_expired');
    expect(mapStuckLockError(refused(Code.Internal)).code).toBe('unavailable');
  });

  // Releasing twice reports NOT_FOUND or a state conflict; both must read as "the
  // lock is no longer stuck" rather than a generic failure.
  it.each([Code.FailedPrecondition, Code.Aborted])('reads %s as a lock conflict', (code) => {
    expect(mapStuckLockError(refused(code)).code).toBe('conflict');
  });

  // ReleaseEmergencyLock re-authorizes the lock's definition (emergency_stuck.go:157),
  // so it can answer definition_not_found too — which is NOT "the lock is gone".
  it('does not report definition_not_found as a released lock', () => {
    const failure = mapStuckLockError(refused(Code.NotFound, 'definition_not_found'));

    expect(failure.code).toBe('definition_not_found');
    expect(failure.typed).toBe(true);
    expect(failure.message).not.toContain('已被释放');
  });

  // A 404 on the LIST is about the definition filter, not about a released lock.
  it('explains a list-path 404 as a filter problem', () => {
    const failure = mapStuckLockListError(refused(Code.NotFound));

    expect(failure.code).toBe('definition_not_found');
    expect(failure.message).toContain('过滤条件');
    expect(failure.message).not.toContain('已被释放');
  });

  it('keeps the typed flag from the header on the list path', () => {
    const typed = mapStuckLockListError(refused(Code.NotFound, 'definition_not_found'));

    expect(typed.code).toBe('definition_not_found');
    expect(typed.typed).toBe(true);
    expect(mapStuckLockListError(refused(Code.PermissionDenied)).message).toBe(
      mapStuckLockError(refused(Code.PermissionDenied)).message,
    );
  });

  // The shared catalog owns the copy for a known reason code; without this the
  // release_busy refusal would read as the local "the lock is no longer stuck".
  it('prefers the centralised copy over the local fallback', () => {
    const failure = mapStuckLockError(refused(Code.FailedPrecondition, 'release_busy'));

    expect(failure.message).toContain('有正在进行的操作');
    expect(failure.message).not.toContain('不再处于卡住状态');
  });

  it('names the three emergency actions', () => {
    expect(actionLabel(EmergencyAction.SET_CONTAINER_IMAGE)).toBe('设置容器镜像');
    expect(actionLabel(EmergencyAction.SET_REPLICAS)).toBe('设置副本数');
    expect(actionLabel(EmergencyAction.SET_APPROVED_ANNOTATION)).toBe('设置批准注解');
    expect(actionLabel(EmergencyAction.UNSPECIFIED)).toBe('未知动作');
  });
});
